package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"insider-notification/internal/logger"
	"insider-notification/internal/metrics"
	"insider-notification/internal/models"
	"insider-notification/internal/ratelimit"
	"insider-notification/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

type Handler struct {
	Repo    *repository.DB
	Redis   *redis.Client
	Limiter *ratelimit.Limiter
}

// İstemci IP'sini güvenli şekilde alan yardımcı fonksiyon
func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	} else {
		ip = strings.Split(ip, ",")[0]
	}
	return strings.TrimSpace(ip)
}

// @Summary Tekil bildirim oluşturur
// @Description Transactional Outbox pattern kullanarak asenkron tekil bildirim işler.
// @Tags notifications
// @Accept json
// @Produce json
// @Param request body models.NotificationRequest true "Bildirim Bilgileri"
// @Success 202 {object} map[string]interface{} "Bildirim alindi, isleniyor"
// @Failure 400 {string} string "Gecersiz JSON veya bilinmeyen alan"
// @Failure 409 {object} map[string]string "Idempotency key cakismasi"
// @Failure 429 {string} string "Rate limit asildi"
// @Failure 500 {string} string "Veritabanina yazilamadi"
// @Router /notifications [post]
func (h *Handler) HandleCreateNotification(w http.ResponseWriter, r *http.Request) {
	// Kafka'nın varsayılan message.max.bytes limiti 1 MB'dır.
	// HTTP isteğini tam olarak 1 MB (1048576 byte) ile sınırlandırıyoruz.
	// Bu sınırı aşan istekler API tarafından anında 413 Payload Too Large ile reddedilir.
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)

	// 2 - Katı JSON Doğrulaması (Strict Validation)
	var req models.NotificationRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // Beklenmeyen bir alan gelirse hata ver

	if err := dec.Decode(&req); err != nil {
		jsonError(w, "Gecersiz JSON veya bilinmeyen alan", http.StatusBadRequest)
		return
	}
	// İçerik Doğrulama (Content Validation)
	if err := req.Validate(); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// ---> [YENİ]: TEKİL İŞLEM RATE LIMIT (1 Jeton Harcar) <---
	ip := getClientIP(r)
	allowed, err := h.Limiter.AllowTokens(r.Context(), "api_tokens:"+ip, 1, 1000, 100)

	if err != nil {
		slog.ErrorContext(r.Context(), "Token Bucket Limiter hatasi, fail-open devreye girdi", "err", err)
	} else if !allowed {
		slog.WarnContext(r.Context(), "Tekil istek Rate Limit asimi", "ip", ip)
		jsonError(w, "Rate limit asildi", http.StatusTooManyRequests)
		return
	}
	// ---------------------------------------------------------

	notifID := uuid.New().String()

	// Outbox pattern ile veritabanına yaz
	err = h.Repo.CreateNotificationWithOutbox(r.Context(), req, notifID)
	if err != nil {
		// ---> EKLENEN KISIM: Idempotency (Unique Violation) Kontrolü <---
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // 23505: unique_violation
			slog.WarnContext(r.Context(), "Idempotency key cakismasi", "key", req.IdempotencyKey)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict) // 409 Conflict
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "conflict",
				"message": "Bu idempotency_key ile kayit zaten mevcut ve isleme alindi.",
			})
			return
		}
		// -----------------------------------------------------------------
		// Çifte istek kontrolü vs. burada yapılabilir
		jsonError(w, "Veritabanina yazilamadi: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Bildirim alindi, isleniyor",
		"id":      notifID,
	})
	slog.InfoContext(r.Context(), "Bildirim veritabanina yazildi", "id", notifID)
}

// @Summary Toplu bildirim oluşturur
// @Description Transactional Outbox pattern kullanarak asenkron toplu bildirim (maksimum 1000 adet) işler.
// @Tags notifications
// @Accept json
// @Produce json
// @Param request body []models.NotificationRequest true "Bildirim Listesi"
// @Success 202 {object} map[string]interface{} "Toplu bildirimler alindi, isleniyor"
// @Failure 400 {string} string "Gecersiz JSON array formatı, bos liste veya limit asimi"
// @Failure 409 {object} map[string]string "Idempotency key cakismasi"
// @Failure 429 {string} string "Rate limit asildi"
// @Failure 500 {string} string "Toplu veritabani yazma islemi basarisiz"
// @Router /notifications/batch [post]
func (h *Handler) HandleCreateNotificationBatch(w http.ResponseWriter, r *http.Request) {
	// Kafka'nın varsayılan message.max.bytes limiti 1 MB'dır.
	// HTTP isteğini tam olarak 1 MB (1048576 byte) ile sınırlandırıyoruz.
	// Bu sınırı aşan istekler API tarafından anında 413 Payload Too Large ile reddedilir.
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)

	// Dikkat: Bu sefer tek bir struct değil, struct dizisi (slice) bekliyoruz
	var reqs []models.NotificationRequest

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // Katı kural: Fazladan alan gelirse reddet

	if err := dec.Decode(&reqs); err != nil {
		jsonError(w, "Gecersiz JSON array formatı veya bilinmeyen alan", http.StatusBadRequest)
		return
	}

	// Mülakattaki "Up to 1000 notifications" kuralının matematiksel kontrolü
	if len(reqs) == 0 {
		jsonError(w, "Bildirim listesi bos olamaz", http.StatusBadRequest)
		return
	}
	if len(reqs) > 1000 {
		jsonError(w, "Tek seferde maksimum 1000 bildirim gonderebilirsiniz", http.StatusBadRequest)
		return
	}

	// ---------------------------------------------------------
	weight := len(reqs)

	// ---> [YENİ]: AĞIRLIK BAZLI RATE LIMIT (Token Bucket) <---
	// Kapasite: 1000 (Burst), Dolum: 100/saniye
	ip := getClientIP(r)
	allowed, err := h.Limiter.AllowTokens(r.Context(), "api_tokens:"+ip, weight, 1000, 100)

	if err != nil {
		// Fail-Open: Redis çökerse trafiği kesme, sadece logla!
		slog.ErrorContext(r.Context(), "Token Bucket Limiter hatasi, fail-open devreye girdi", "err", err)
	} else if !allowed {
		slog.WarnContext(r.Context(), "Toplu istek Rate Limit asimi", "ip", ip, "requested_tokens", weight)
		jsonError(w, "Rate limit asildi (Kovanizda yeterli jeton yok)", http.StatusTooManyRequests)
		return
	}
	// ---------------------------------------------------------

	// [YENİ EKLENEN] Toplu İçerik Doğrulama
	for i := range reqs {
		if err := reqs[i].Validate(); err != nil {
			// Hangi satırda hata olduğunu istemciye söyleyelim
			errMsg := fmt.Sprintf("Satir %d icin dogrulama hatasi: %v", i+1, err)
			jsonError(w, errMsg, http.StatusBadRequest)
			return
		}
	}
	batchID := uuid.New().String()
	// Yeni yazdığımız Batch DB fonksiyonunu çağırıyoruz
	err = h.Repo.CreateNotificationBatchWithOutbox(r.Context(), reqs, batchID)
	if err != nil {
		// ---> [DÜZELTME]: Toplu İşlem (Batch) İçin Idempotency Kontrolü <---
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // 23505: unique_violation
			slog.WarnContext(r.Context(), "Toplu islemde idempotency key cakismasi", "batch_id", batchID)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict) // 409 Conflict
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "conflict",
				"message": "Paket icindeki idempotency_key degerlerinden en az biri sistemde zaten mevcut veya pakette mukerrer kayit var.",
			})
			return
		}
		// -----------------------------------------------------------------

		jsonError(w, "Toplu veritabani yazma islemi basarisiz: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":  "Toplu bildirimler alindi, isleniyor",
		"count":    len(reqs),
		"batch_id": batchID,
	})
}

// @Summary Bildirim İptal Et
// @Description Bekleyen veya tekrar denenecek bir bildirimi iptal eder. Redis fast-fail bayrağı ile Kafka'daki işlenmesini durdurur.
// @Tags notifications
// @Produce json
// @Param id path string true "Notification ID"
// @Success 200 {object} map[string]string "Bildirim basariyla iptal edildi"
// @Failure 400 {object} map[string]string "Gecersiz ID formati"
// @Failure 404 {string} string "Bildirim bulunamadi"
// @Failure 409 {object} map[string]string "Bildirim terminal durumda, iptal edilemez"
// @Router /notifications/{id} [delete]
func (h *Handler) HandleCancelNotification(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id") // Go 1.22 URL Path variable okuma
	if !validateUUID(w, id) {
		return
	}

	// 1. Veritabanından iptal et
	canceled, currentStatus, err := h.Repo.CancelNotification(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "Bildirim iptal DB hatasi", "id", id, "error", err)
		jsonError(w, "Veritabani hatasi", http.StatusInternalServerError)
		return
	}

	if !canceled {
		if currentStatus == "" {
			// Kayıt hiç yok
			jsonError(w, "Bildirim bulunamadi", http.StatusNotFound)
			return
		}
		// Kayıt var ama terminal durumda (delivered, canceled, failed_permanently)
		slog.WarnContext(r.Context(), "Iptal edilemez durumdaki bildirim icin iptal istegi",
			"id", id, "current_status", currentStatus)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error":          "cancel_not_allowed",
			"message":        fmt.Sprintf("Bildirim '%s' durumunda, iptal edilemez", currentStatus),
			"current_status": currentStatus,
		})
		return
	}

	// 2. Redis'e fast-fail bayrağını at (Worker bu mesaji Kafka'dan alinca çöpe atacak)
	if err := h.Redis.Set(r.Context(), "cancel_event:"+id, "true", 24*time.Hour).Err(); err != nil {
		slog.WarnContext(r.Context(),
			"Redis cancel flag atanamadi, worker iptal gormeyebilir",
			"id", id, "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Bildirim basariyla iptal edildi"})
}

// @Summary Tekil Bildirim Getir
// @Description ID'ye göre bildirimin detaylarını ve son durumunu getirir.
// @Tags notifications
// @Param id path string true "Notification ID"
// @Produce json
// @Success 200 {object} models.NotificationResponse
// @Failure 404 {string} string "Bildirim bulunamadi"
// @Router /notifications/{id} [get]
func (h *Handler) HandleGetNotification(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validateUUID(w, id) {
		return
	}

	notif, err := h.Repo.GetNotificationByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, "Bildirim bulunamadi", http.StatusNotFound)
		} else {
			jsonError(w, "Veritabani hatasi", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(notif)
}

// @Summary Bildirimleri Listele
// @Description Filtreleme ve Keyset Cursor destekli yüksek performanslı bildirim listesi getirir.
// @Tags notifications
// @Param status query string false "Duruma göre filtrele (pending, processing, delivered, failed_retrying, failed_permanently, canceled)"
// @Param channel query string false "Kanala göre filtrele (sms, email, push)"
// @Param start_date query string false "Başlangıç tarihi filtresi (ISO8601 veya YYYY-MM-DD)"
// @Param end_date query string false "Bitiş tarihi filtresi (ISO8601 veya YYYY-MM-DD)"
// @Param limit query int false "Sayfa boyutu (varsayılan 10, max 100)"
// @Param cursor query string false "Bir sonraki sayfa için imleç (next_cursor)"
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /notifications [get]
func (h *Handler) HandleListNotifications(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	channel := r.URL.Query().Get("channel")

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	startDateStr := r.URL.Query().Get("start_date")
	endDateStr := r.URL.Query().Get("end_date")

	var startDate, endDate *time.Time

	if startDateStr != "" {
		parsedStart, err := parseDateRobustly(startDateStr)
		if err != nil {
			jsonError(w, "start_date formati hatali. (ISO8601 veya YYYY-MM-DD bekleniyor)", http.StatusBadRequest)
			return
		}
		startDate = &parsedStart
	}

	if endDateStr != "" {
		parsedEnd, err := parseDateRobustly(endDateStr)
		if err != nil {
			jsonError(w, "end_date formati hatali. (ISO8601 veya YYYY-MM-DD bekleniyor)", http.StatusBadRequest)
			return
		}

		// Sadece tarih (YYYY-MM-DD) girildiyse, o günün son saniyesini (23:59:59) kapsayacak şekilde genişletiyoruz
		if len(endDateStr) == 10 {
			parsedEnd = parsedEnd.Add(24*time.Hour - time.Nanosecond)
		}
		endDate = &parsedEnd
	}

	// [YENİ] Mantıksal Validasyon
	if startDate != nil && endDate != nil && startDate.After(*endDate) {
		jsonError(w, "start_date, end_date'den daha ileri bir tarih olamaz", http.StatusBadRequest)
		return
	}

	// ---> [YENİ]: CURSOR PARSE İŞLEMİ <---
	cursor := r.URL.Query().Get("cursor")
	var cursorTime *time.Time
	var cursorID string

	if cursor != "" {
		decoded, err := base64.StdEncoding.DecodeString(cursor)
		if err == nil {
			parts := strings.Split(string(decoded), "|")
			if len(parts) == 2 {
				// RFC3339Nano formatında şifrelediğimiz tarihi geri çözüyoruz
				if t, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
					cursorTime = &t
					cursorID = parts[1]
				}
			}
		}
		if cursorTime == nil || cursorID == "" {
			jsonError(w, "Gecersiz cursor formati", http.StatusBadRequest)
			return
		}
	}

	// Veritabanından veriyi ve bir sonraki sayfanın cursor'unu çek
	list, nextCursor, err := h.Repo.ListNotifications(r.Context(), status, channel, startDate, endDate, cursorTime, cursorID, limit)
	if err != nil {
		jsonError(w, "Listeleme hatasi", http.StatusInternalServerError)
		return
	}

	if list == nil {
		list = []models.NotificationResponse{}
	}

	// Standart REST best-practice: Meta verilerle birlikte JSON dön
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": list,
		"meta": map[string]interface{}{
			"limit":       limit,
			"next_cursor": nextCursor,
			"has_more":    nextCursor != "", // Ön yüzde (UI) kolaylık sağlar
		},
	})
}

// Tarihi hem saatli hem saatsiz formatta çözebilen yardımcı fonksiyon
func parseDateRobustly(dateStr string) (time.Time, error) {
	// Önce tam zamanlı RFC3339 (ISO8601) deneriz
	if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
		return t, nil
	}
	// Olmazsa sadece Tarih kısmını deneriz
	return time.Parse(time.DateOnly, dateStr)
}

// @Summary Toplu Bildirim Durumunu Getir
// @Description Batch ID'ye ait tüm bildirimlerin durumlarını listeler.
// @Tags notifications
// @Param id path string true "Batch ID"
// @Produce json
// @Success 200 {array} models.NotificationResponse
// @Router /notifications/batch/{id} [get]
func (h *Handler) HandleGetBatchStatus(w http.ResponseWriter, r *http.Request) {
	batchID := r.PathValue("id")
	if !validateUUID(w, batchID) {
		return
	}

	list, err := h.Repo.GetNotificationsByBatchID(r.Context(), batchID)
	if err != nil {
		jsonError(w, "Veritabani hatasi", http.StatusInternalServerError)
		return
	}

	if list == nil {
		list = []models.NotificationResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"batch_id":      batchID,
		"total_count":   len(list),
		"notifications": list,
	})
}

// @Summary Sistem Sağlık Durumu
// @Description Veritabanı ve Redis bağlantılarını anlık kontrol eder.
// @Tags system
// @Produce json
// @Success 200 {object} map[string]string "Tüm sistemler ayakta"
// @Failure 503 {object} map[string]string "Servislerden biri veya daha fazlası çökmüş"
// @Router /health [get]
func (h *Handler) HandleHealthCheck(w http.ResponseWriter, r *http.Request) {
	// Bağlantıların yanıt vermesini en fazla 3 saniye bekleriz
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	status := http.StatusOK
	response := map[string]string{"api": "up", "database": "up", "redis": "up"}

	// Veritabanı (PostgreSQL) Kontrolü
	if err := h.Repo.Pool.Ping(ctx); err != nil {
		response["database"] = "down"
		status = http.StatusServiceUnavailable
	}

	// In-Memory DB (Redis) Kontrolü
	if err := h.Redis.Ping(ctx).Err(); err != nil {
		response["redis"] = "down"
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response)
}

func SetupRoutes(repo *repository.DB, rdb *redis.Client) *http.ServeMux {
	h := &Handler{
		Repo:    repo,
		Redis:   rdb,
		Limiter: ratelimit.NewLimiter(rdb),
	}
	mux := http.NewServeMux()

	// Her endpoint'i kendi statik PATTERN'i ile sarıyoruz
	mux.HandleFunc("POST /notifications", MeasureLatency("/notifications", h.HandleCreateNotification))
	mux.HandleFunc("POST /notifications/batch", MeasureLatency("/notifications/batch", h.HandleCreateNotificationBatch))

	// DİKKAT: Buraya "/notifications/{id}" statik metnini vererek Prometheus'u kardinalite krizinden kurtarıyoruz
	mux.HandleFunc("GET /notifications/{id}", MeasureLatency("/notifications/{id}", h.HandleGetNotification))
	mux.HandleFunc("DELETE /notifications/{id}", MeasureLatency("/notifications/{id}", h.HandleCancelNotification))

	mux.HandleFunc("GET /notifications", MeasureLatency("/notifications", h.HandleListNotifications))
	mux.HandleFunc("GET /notifications/batch/{id}", MeasureLatency("/notifications/batch/{id}", h.HandleGetBatchStatus))
	mux.HandleFunc("GET /health", MeasureLatency("/health", h.HandleHealthCheck))
	mux.HandleFunc("GET /swagger", h.HandleSwaggerDocs)
	mux.HandleFunc("GET /swagger/", h.HandleSwaggerDocs)

	mux.Handle("GET /metrics", promhttp.Handler())

	return mux
}

// HandleSwaggerDocs, API uzerinden Swagger UI ve dokuman dosyalarini servis eder.
func (h *Handler) HandleSwaggerDocs(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/swagger", "/swagger/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head>
    <meta charset="utf-8" />
    <title>Notification API - Swagger</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script>
      window.ui = SwaggerUIBundle({
        url: "/swagger/swagger.json",
        dom_id: "#swagger-ui",
      });
    </script>
  </body>
</html>`))
		return
	case "/swagger/swagger.json":
		serveIfExists(w, r, "docs/swagger.json")
		return
	case "/swagger/swagger.yaml":
		serveIfExists(w, r, "docs/swagger.yaml")
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func serveIfExists(w http.ResponseWriter, r *http.Request, filePath string) {
	if _, err := os.Stat(filePath); err != nil {
		jsonError(w, "Swagger dokumani bulunamadi", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, filePath)
}

func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)

		// Kendi özel anahtarımızla gömüyoruz
		ctx := context.WithValue(r.Context(), logger.CorrelationIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RateLimitMiddleware, API'ye gelen istekleri IP bazında sınırlandırır.
// ---> [MİMARİ DÜZELTME]: Global Flood (DDoS) Koruması <---
// Bu katman artık "İş Mantığı (Business Quota)" katmanı DEĞİLDİR.
// Sadece tek bir IP'den gelen anlamsız HTTP spam'lerini (Flood) engellemek için
// limiti 100'den 500'e çıkarılmış ve key'i ayrıştırılmıştır.
// Gerçek iş mantığı kotaları (100 bildirim/sn), handler içindeki Token Bucket'a bırakılmıştır.
func RateLimitMiddleware(limiter *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Gerçek IP Adresini Bul
			ip := r.Header.Get("X-Forwarded-For")
			if ip == "" {
				ip, _, _ = net.SplitHostPort(r.RemoteAddr)
			} else {
				ip = strings.Split(ip, ",")[0]
			}
			ip = strings.TrimSpace(ip)

			// 2. Sliding Window: Saniyede 500 HTTP İsteği (Global Flood Limiti)
			// Key ismini "api_ip:" yerine "global_flood:" yaparak Token Bucket'tan tamamen yalıtıyoruz.
			allowed, err := limiter.AllowSliding(r.Context(), "global_flood:"+ip, 500)

			if err != nil {
				// Fail-Open yaklaşımı: Redis çökerse trafiği kesme, sadece logla!
				slog.ErrorContext(r.Context(), "Global Rate Limiter (Redis) coktu, trafige izin veriliyor", "err", err)
				next.ServeHTTP(w, r)
				return
			}
			if !allowed {
				slog.WarnContext(r.Context(), "L7 Global Flood engellendi", "ip", ip)
				jsonError(w, "Cok fazla HTTP istegi gonderdiniz (Global Flood Protection)", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// MeasureLatency, route pattern'ini alıp metrikleri güvenle (High Cardinality olmadan) işler
func MeasureLatency(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// İşlem başlar başlamaz kronometreyi tut
		timer := prometheus.NewTimer(metrics.APIRequestDuration.WithLabelValues(r.Method, route))

		// Fonksiyon (handler) bitince süreyi kaydet
		defer timer.ObserveDuration()

		next(w, r)
	}
}

// jsonError, tutarlı JSON hata formatı ile HTTP hata yanıtı döndürür.
// Tüm endpointlerde aynı response yapısını garanti eder.
func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func validateUUID(w http.ResponseWriter, id string) bool {
	if _, err := uuid.Parse(id); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Gecersiz ID formati (UUID bekleniyor)",
		})
		return false
	}
	return true
}
