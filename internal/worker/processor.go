package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	// "github.com/sony/gobreaker"
	"insider-notification/internal/logger"
	"insider-notification/internal/models"
	"insider-notification/internal/ratelimit"
	"insider-notification/internal/repository"

	"insider-notification/internal/circuitbreaker"
	"insider-notification/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

type Processor struct {
	Repo       *repository.DB
	Redis      *redis.Client
	Limiter    *ratelimit.Limiter
	DLQWriter  *kafka.Writer // Dead Letter Queue için eklenen Writer
	CB         *circuitbreaker.DistributedCB
	WebhookURL string
}

type WebhookResponse struct {
	MessageID  string `json:"messageId"`
	Status     string `json:"status"`
	Timestamp  string `json:"timestamp"`
	HTTPStatus int    `json:"-"` // HTTP Statü kodunu closure dışına taşımak için eklendi
}

// Buradaki kritik detay: "Rate limit aşılırsa Kafka'dan silinir (commit edilir)"
// tasarımımızda Source of Truth (Gerçeğin Kaynağı) veritabanıdır.
// Rate limite takıldığımızda, veritabanına failed_retrying yazarız.
// Yazma başarılı olursa Kafka'dan silmeliyiz (commit);
// çünkü onu tekrar kuyruğa atacak olan sistem (Outbox Relay Cron) veritabanını okuyacaktır.
// Eğer Kafka'da tutmaya devam edersek ve sleep yapmazsak sistem sonsuz bir "rate limit hit" döngüsüne girer.

func (p *Processor) Process(ctx context.Context, msg kafka.Message, tracker *CommitTracker) {
	var payload models.NotificationPayload // Gerçek senaryoda bu Outbox payload'udur
	// [DÜZELTME 8]: Unmarshal hatası yutulmuyor, zehirli mesaj (poison pill) çöpe veya DLQ'ya atılıyor
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		slog.ErrorContext(ctx, "Payload parse edilemedi, mesaj DLQ'ya aliniyor", "error", err)
		_ = p.DLQWriter.WriteMessages(ctx, kafka.Message{Value: msg.Value})
		tracker.MarkDone(ctx, msg)
		return
	}
	// [YENİ EKLENDİ] İzlenebilirliği Yeniden Kurma (Trace Reconstruction)
	// API'de yaratılan CorrelationID'yi Kafka'dan aldık, şimdi Worker'ın context'ine enjekte ediyoruz.
	ctx = context.WithValue(ctx, logger.CorrelationIDKey, payload.CorrelationID)
	// BUNDAN SONRA TÜM LOGLARDA traceCtx KULLANACAĞIZ!
	slog.InfoContext(ctx, "Mesaj Kafka'dan alindi, isleniyor", "id", payload.ID)

	notifID := payload.ID

	if notifID == "" {
		slog.ErrorContext(ctx, "notifID bos geldi, zehirli mesaj atliyor")
		tracker.MarkDone(ctx, msg)
		return
	}

	// 1. CANCEL CHECK (Fast-fail)
	isCanceled, _ := p.Redis.Get(ctx, "cancel_event:"+notifID).Result()
	if isCanceled == "true" {
		slog.InfoContext(ctx, "Mesaj iptal edilmis, islem atlandi", "id", notifID)
		tracker.MarkDone(ctx, msg)
		return
	}

	// [YENİ]: Latency (Gecikme) Ölçümünü Başlat
	timer := prometheus.NewTimer(metrics.EventProcessingDuration.WithLabelValues(payload.Channel))
	defer timer.ObserveDuration() // Fonksiyon bitince süreyi kaydet

	// 2. RATE LIMIT CHECK
	/* [MÜLAKAT NOTU - DÜZELTME 4]:
	Buradaki Rate Limiter (limiter.go) performans için Fixed-Window algoritması kullanır.
	Bu matematiksel olarak saniye sınırlarında (örn: 00:59.999 ile 01:00.001) 2x (200) burst yapabilir.
	Kesin sınır gerekiyorsa Redis EVAL ile Sliding Window Log veya Token Bucket'a çevrilmelidir.
	*/
	allowed, err := p.Limiter.AllowSliding(ctx, payload.Channel, 100)
	if err != nil || !allowed {
		slog.WarnContext(ctx, "Rate limit asildi, bekletiliyor, mesaj geri atilacak", "channel", payload.Channel)
		// Mesajı işleme, status'u DB'de failed_retrying yap (aşağıdaki başarısızlık mantığı ile)
		// Rate limite takıldıysa DB'yi güncelle.
		// Eğer DB güncellemesi başarısız olursa COMMIT ETME!
		if _, dbErr := p.handleFailure(ctx, notifID); dbErr != nil {
			slog.ErrorContext(ctx, "Rate limit hatasi DB'ye yazilamadi, Kafka'da bekletiliyor", "err", dbErr)
			return
		}
		// DB 'failed_retrying' oldu. Artık Kafka'dan silebiliriz. Relay onu tekrar kuyruğa alacak.
		tracker.MarkDone(ctx, msg)
		return
	}

	// 3. OPTIMISTIC CONCURRENCY CONTROL (DB Kilit)
	locked, err := p.Repo.LockNotificationForProcessing(ctx, notifID)
	if err != nil || !locked {
		slog.InfoContext(ctx, "Mesaj baska worker tarafindan islenmis veya durumu uygun degil", "id", notifID)
		tracker.MarkDone(ctx, msg)
		return
	}

	// 4. WEBHOOK ISTEGI
	webhookPayload := map[string]string{
		"to":      payload.Recipient,
		"channel": payload.Channel,
		"content": payload.Content,
	}
	reqBody, _ := json.Marshal(webhookPayload)

	result, err := p.CB.Execute(ctx, func() (interface{}, error) {
		client := http.Client{Timeout: 5 * time.Second}
		// Dış sisteme en fazla 10 saniye tahammül et!
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		// Context'i (ctx) HTTP isteğine entegre ediyoruz
		// Burada artık ZENGİNLEŞTİRİLMİŞ ctx kullanılıyor!
		// Hem graceful shutdown (SIGTERM) iptallerini anlar, hem de izleme ID'sini taşır.
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.WebhookURL, bytes.NewBuffer(reqBody))
		if err != nil {
			return nil, err
		}

		// Post metodu yerine NewRequest kullandığımız için Header'ı manuel ekliyoruz
		req.Header.Set("Content-Type", "application/json")

		// İsteği gönder (Eğer ctx iptal edilirse, clientDo() anında hata döner ve işlemi keser)
		resp, doErr := client.Do(req)
		if doErr != nil {
			return nil, doErr // Network hatası
		}
		// ---> [KRİTİK]: TCP Bağlantısının havuza dönmesini garantile <---
		defer resp.Body.Close()
		// Body'nin içini boşluğa dökerek (Discard) sonuna kadar okunduğundan emin ol!
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			_, _ = io.Copy(io.Discard, resp.Body) // Hata durumunda sızıntıyı önle
			return nil, fmt.Errorf("upstream server error: %d", resp.StatusCode)
		}

		var whResp WebhookResponse
		whResp.HTTPStatus = resp.StatusCode // Statü kodunu struct'a kaydet

		// Body'yi içeride parse et
		_ = json.NewDecoder(resp.Body).Decode(&whResp)

		// Ne kaldiysa boşluğa dök (Drain) ki TCP bağlantısı havuza güvenle dönsün!
		_, _ = io.Copy(io.Discard, resp.Body)

		// Sadece parse edilmiş struct'ı döndür
		return &whResp, nil
	})

	if err != nil {
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			slog.WarnContext(ctx, "Devre Kesici ACIK (Fast-fail), webhook'a gidilmedi")
		} else {
			slog.ErrorContext(ctx, "Webhook istegi basarisiz oldu", "error", err)
		}
	}

	// Closure dışına taşınan sonucu al
	var whResp *WebhookResponse
	if err == nil && result != nil {
		whResp = result.(*WebhookResponse)
	}

	// 5. SONUCU VERITABANINA YAZ
	var dbErr error
	var isPermanentFail bool

	if err == nil && whResp != nil && (whResp.HTTPStatus == http.StatusAccepted || whResp.HTTPStatus == http.StatusOK || whResp.HTTPStatus == http.StatusNoContent) {
		var externalID *string

		if whResp.MessageID != "" {
			externalID = &whResp.MessageID
			slog.InfoContext(ctx, "Webhook tarafindan basariyla kabul edildi", "external_id", *externalID)
		}
		_, dbErr = p.Repo.Pool.Exec(ctx, `
			UPDATE notifications 
			SET status = 'delivered', external_id = $1, updated_at = NOW() 
			WHERE id = $2
		`, externalID, notifID)

		if dbErr == nil {
			slog.InfoContext(ctx, "Mesaj basariyla iletildi", "id", notifID)
			// Başarılı İşlem Sayacını Artır
			metrics.EventsProcessed.WithLabelValues("delivered", payload.Channel).Inc()
		}
	} else {
		isPermanentFail, dbErr = p.handleFailure(ctx, notifID) // handleFailure fonksiyonunun da error dönmesini saglamalisin
		// Hatalı İşlem Sayacını Artır
		if isPermanentFail {
			metrics.EventsProcessed.WithLabelValues("failed_permanently", payload.Channel).Inc()
		} else {
			metrics.EventsProcessed.WithLabelValues("retrying", payload.Channel).Inc()
		}
	}

	// 6. KAFKA OFFSET COMMIT (SADECE DB YAZMASI BAŞARILIYSA!)
	if dbErr != nil {
		slog.ErrorContext(ctx, "Veritabanina nihai durum YAZILAMADI, mesaj Kafka'da bekletilecek (Retry)", "id", notifID, "error", dbErr)
		// Commit ETMİYORUZ. Worker bu fonksiyondan çıkar.
		// Kafka kısa süre sonra (timeout) bu mesajı başka/aynı worker'a TEKRAR gönderir.
		return // KESİNLİKLE COMMIT ETME
	}

	if isPermanentFail {
		slog.WarnContext(ctx, "Mesaj kalici olarak basarisiz oldu, DLQ'ya atiliyor", "id", notifID)
		dlqErr := p.DLQWriter.WriteMessages(ctx, kafka.Message{Key: []byte(notifID), Value: msg.Value})
		if dlqErr != nil {
			slog.ErrorContext(ctx, "DLQ yazmasi basarisiz, islem geri sariliyor", "id", notifID, "err", dlqErr)
			return // DLQ'ya yazamazsak commit etmiyoruz ki kaybolmasin!
		}
	}

	// Veritabanı güvenli, karşı taraf güvenli. Artık Commit edebiliriz.
	tracker.MarkDone(ctx, msg)
}

func (p *Processor) handleFailure(ctx context.Context, id string) (bool, error) {
	var newStatus string
	err := p.Repo.Pool.QueryRow(ctx, `
		UPDATE notifications 
		SET retry_count = retry_count + 1,
			status = CASE WHEN retry_count + 1 >= 3 THEN 'failed_permanently' ELSE 'failed_retrying' END,
			next_retry_at = CASE WHEN retry_count + 1 >= 3 THEN NULL ELSE NOW() + (POWER(2, retry_count) * INTERVAL '1 minute') END,
			updated_at = NOW()
		WHERE id = $1
		RETURNING status
	`, id).Scan(&newStatus)

	return newStatus == "failed_permanently", err
}
