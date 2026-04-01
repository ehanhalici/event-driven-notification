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

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	"insider-notification/internal/circuitbreaker"
	"insider-notification/internal/logger"
	"insider-notification/internal/metrics"
	"insider-notification/internal/models"
	"insider-notification/internal/ratelimit"
	"insider-notification/internal/repository"
)

type Processor struct {
	Repo       *repository.DB
	Redis      *redis.Client
	Limiter    *ratelimit.Limiter
	DLQWriter  *kafka.Writer // Dead Letter Queue için eklenen Writer
	CB         *circuitbreaker.DistributedCB
	WebhookURL string
	MaxRetries int
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

// Buradaki kritik detay: "Rate limit aşılırsa Kafka'dan silinir (commit edilir)"
// tasarımımızda Source of Truth (Gerçeğin Kaynağı) veritabanıdır.
// Rate limite takıldığımızda, veritabanına failed_retrying yazarız ama HAK TÜKETMEYİZ.
// Yazma başarılı olursa Kafka'dan silmeliyiz (commit);
// çünkü onu tekrar kuyruğa atacak olan sistem (Outbox Relay Cron) veritabanını okuyacaktır.

func (p *Processor) Process(ctx context.Context, msg kafka.Message, tracker *CommitTracker) error {
	var payload models.NotificationPayload

	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		slog.ErrorContext(ctx, "Payload parse edilemedi, mesaj DLQ'ya aliniyor", "error", err)
		dlqErr := p.DLQWriter.WriteMessages(ctx, kafka.Message{Value: msg.Value})
		if dlqErr != nil {
			return fmt.Errorf("poison pill DLQ write error: %w", dlqErr)
		}
		metrics.KafkaDLQMessagesTotal.Inc()
		tracker.MarkDone(ctx, msg)
		return nil
	}

	ctx = context.WithValue(ctx, logger.CorrelationIDKey, payload.CorrelationID)
	slog.InfoContext(ctx, "Mesaj Kafka'dan alindi, isleniyor", "id", payload.ID)

	metrics.KafkaMessagesConsumedTotal.WithLabelValues(msg.Topic).Inc()

	notifID := payload.ID
	if notifID == "" {
		slog.ErrorContext(ctx, "notifID bos geldi, zehirli mesaj atliyor")
		tracker.MarkDone(ctx, msg)
		return nil
	}

	// 1. CANCEL CHECK (Fast-fail)
	isCanceled, _ := p.Redis.Get(ctx, "cancel_event:"+notifID).Result()
	if isCanceled == "true" {
		slog.InfoContext(ctx, "Mesaj iptal edilmis, islem atlandi", "id", notifID)
		tracker.MarkDone(ctx, msg)
		return nil
	}

	timer := prometheus.NewTimer(metrics.EventProcessingDuration.WithLabelValues(payload.Channel))
	defer timer.ObserveDuration()

	// 2. OPTIMISTIC CONCURRENCY CONTROL (DB Kilit)
	locked, err := p.Repo.LockNotificationForProcessing(ctx, notifID)
	if err != nil {
		return fmt.Errorf("veritabanina erisilemedi, kilit alinamadi id: %s, error: %w", notifID, err)
	}

	if !locked {
		// KAYIP DLQ MESAJINI KURTARMA 
		// Eğer sistem DB'ye failed_permanently yazıp DLQ'ya atamadan çöktüyse,
		// burada Idempotency Skip yiyecektir. Bu durumu tespit edip DLQ'yu garantiye alıyoruz.
		var currentStatus string
		dbErr := p.Repo.Pool.QueryRow(ctx, "SELECT status FROM notifications WHERE id = $1", notifID).Scan(&currentStatus)
		if dbErr != nil {
			if errors.Is(dbErr, pgx.ErrNoRows) {
				// Mesaj veritabanında hiç yok Atla.
				tracker.MarkDone(ctx, msg)
				return nil
			}
			// DB'ye ulaşılamadı. Kafka'dan SİLMEMELİYİZ!
			return fmt.Errorf("idempotency status check failed: %w", dbErr)
		}
		if currentStatus == "failed_permanently" {
			slog.WarnContext(ctx, "Mesaj kalici hata durumunda ama Kafka offset'i silinmemis, DLQ garantisi icin tekrar yaziliyor", "id", notifID)
			dlqErr := p.DLQWriter.WriteMessages(ctx, kafka.Message{
				Key:   []byte(payload.ID),
				Value: msg.Value,
			})
			if dlqErr != nil {
				return fmt.Errorf("fatal DLQ write retry error: %w", dlqErr) // Fail-fast pod restart
			}
			metrics.KafkaDLQMessagesTotal.Inc()
		} else {
			slog.InfoContext(ctx, "Mesaj baska worker tarafindan isleniyor veya durumu uygun degil (Idempotency Skip)", "id", notifID)
		}

		tracker.MarkDone(ctx, msg)
		return nil 
	}

	// 3. RATE LIMIT CHECK
	allowed, err := p.Limiter.AllowSliding(ctx, payload.Channel, 100)
	if err != nil || !allowed {
		slog.WarnContext(ctx, "Rate limit asildi, bekletiliyor", "channel", payload.Channel)
		metrics.RateLimitHitsTotal.WithLabelValues("channel_sliding").Inc()

		var updatedStatus string
		dbErr := p.Repo.Pool.QueryRow(ctx, `
			UPDATE notifications 
			SET status = 'failed_retrying',
				next_retry_at = NOW() + INTERVAL '10 seconds',
				updated_at = NOW()
			WHERE id = $1 
			  AND status = 'processing'
			  AND status NOT IN ('delivered', 'failed_permanently')
			RETURNING status
		`, notifID).Scan(&updatedStatus)

		if errors.Is(dbErr, pgx.ErrNoRows) {
			slog.WarnContext(ctx, "Rate limit backoff atlandi: bildirim artik processing degil (idempotent skip)", "id", notifID)
		} else if dbErr != nil {
			slog.ErrorContext(ctx, "KRITIK HATA: Rate limit durumu DB'ye yazilamadi", "error", dbErr)
			tracker.MarkPoisoned(dbErr)
			return fmt.Errorf("rate limit db error: %w", dbErr)
		}

		tracker.MarkDone(ctx, msg)
		return nil
	}
	
	// 4. WEBHOOK ISTEGI
	webhookPayload := map[string]string{
		"to":      payload.Recipient,
		"channel": payload.Channel,
		"content": payload.Content,
	}
	reqBody, _ := json.Marshal(webhookPayload)

	webhookTimer := prometheus.NewTimer(metrics.WebhookCallDuration.WithLabelValues(payload.Channel))

	result, err := p.CB.Execute(ctx, func() (interface{}, error) {
		client := http.Client{Timeout: 5 * time.Second}
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodPost, p.WebhookURL, bytes.NewBuffer(reqBody))
		if reqErr != nil {
			return nil, reqErr
		}

		req.Header.Set("Content-Type", "application/json")

		resp, doErr := client.Do(req)
		if doErr != nil {
			return nil, doErr
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
			// Sadece 5xx, 429 ve 408 tekrar denenebilir (Retriable) hatalardır. CB'yi tetikler.
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil, fmt.Errorf("upstream server error (retriable): %d", resp.StatusCode)
		}
		
		var whResp WebhookResponse
		whResp.HTTPStatus = resp.StatusCode
		_ = json.NewDecoder(resp.Body).Decode(&whResp)
		_, _ = io.Copy(io.Discard, resp.Body)

		return &whResp, nil
	})

	webhookTimer.ObserveDuration()

	if err != nil {
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			slog.WarnContext(ctx, "Devre Kesici ACIK (Fast-fail), webhook'a gidilmedi")
			metrics.WebhookCallsTotal.WithLabelValues(payload.Channel, "circuit_open").Inc()
			metrics.CircuitBreakerState.Set(1)
		} else {
			slog.ErrorContext(ctx, "Webhook istegi basarisiz oldu", "error", err)
			metrics.WebhookCallsTotal.WithLabelValues(payload.Channel, "error").Inc()
		}
	} else {
		metrics.WebhookCallsTotal.WithLabelValues(payload.Channel, "success").Inc()
		metrics.CircuitBreakerState.Set(0)
	}

	var whResp *WebhookResponse
	if err == nil && result != nil {
		whResp = result.(*WebhookResponse)
	}

	// 5. SONUCU VERITABANINA YAZ
	var dbErr error
	var isPermanentFail bool

	if err == nil && whResp != nil {
		if whResp.HTTPStatus >= 200 && whResp.HTTPStatus < 300 {
			// BAŞARILI (200, 201, 202, 204)
			var externalID *string
			if whResp.MessageID != "" {
				externalID = &whResp.MessageID
				slog.InfoContext(ctx, "Webhook tarafindan basariyla kabul edildi", "external_id", *externalID)
			}

			var updatedStatus string
			dbErr = p.Repo.Pool.QueryRow(ctx, `
			    UPDATE notifications 
			    SET status = 'delivered', external_id = $1, updated_at = NOW() 
			    WHERE id = $2 
			      AND status = 'processing'
			      AND status NOT IN ('delivered', 'failed_permanently')
			    RETURNING status
     		`, externalID, notifID).Scan(&updatedStatus)

			if dbErr == nil {
				slog.InfoContext(ctx, "Mesaj basariyla iletildi", "id", notifID)
				metrics.EventsProcessed.WithLabelValues("delivered", payload.Channel).Inc()
			} else if errors.Is(dbErr, pgx.ErrNoRows) {
				slog.WarnContext(ctx, "Bildirim isleme sirasinda iptal edildi veya zaten nihai durumda", "id", notifID)
				dbErr = nil
			}
		} else {
			// HTTP 400, 401, 403, 404 (KULLANICI HATALARI - KALICI HATA)
			// Tekrar denemeye gerek yok, direkt failed_permanently yap!
			slog.WarnContext(ctx, "Webhook kalici hata dondu (Orn: 400 Bad Request)", "status", whResp.HTTPStatus, "id", notifID)
			
			// handleFailure'a MaxRetries parametresi olarak 0 gönderiyoruz ki direkt failed_permanently olsun!
			isPermanentFail, dbErr = p.handleFailure(ctx, notifID, 0) 
			metrics.EventsProcessed.WithLabelValues("failed_permanently", payload.Channel).Inc()
		}
	} else {
		// CB'den hata döndü (5xx, 429, Timeout veya Network Error) -> Normal Retry
		isPermanentFail, dbErr = p.handleFailure(ctx, notifID, p.MaxRetries)
		if isPermanentFail {
			metrics.EventsProcessed.WithLabelValues("failed_permanently", payload.Channel).Inc()
		} else {
			metrics.EventsProcessed.WithLabelValues("retrying", payload.Channel).Inc()
		}
	}
	// 6. KAFKA OFFSET COMMIT
	if dbErr != nil {
		slog.ErrorContext(ctx, "KRITIK HATA: Veritabanina nihai durum YAZILAMADI.", "id", notifID, "error", dbErr)
		tracker.MarkPoisoned(dbErr)
		return fmt.Errorf("final db update failed: %w", dbErr)
	}

	if isPermanentFail {
		slog.WarnContext(ctx, "Mesaj kalici olarak basarisiz oldu, DLQ'ya atiliyor", "id", notifID)
		dlqErr := p.DLQWriter.WriteMessages(ctx, kafka.Message{
			Key:   []byte(payload.ID),
			Value: msg.Value,
		})

		if dlqErr != nil {
			slog.ErrorContext(ctx, "CRITICAL: DLQ'ya yazilamadi, veri kaybi riski! Worker durduruluyor (Fail-Fast)",
				"error", dlqErr,
				"message_id", payload.ID,
			)
			tracker.MarkPoisoned(dlqErr)
			return fmt.Errorf("fatal DLQ write error: %w", dlqErr)
		}

		metrics.KafkaDLQMessagesTotal.Inc()
		tracker.MarkDone(ctx, msg)
		return nil 
	}

	tracker.MarkDone(ctx, msg)
	return nil
}

func (p *Processor) handleFailure(ctx context.Context, id string, maxRetries int) (bool, error) {
	var newStatus string
	err := p.Repo.Pool.QueryRow(ctx, `
		UPDATE notifications 
		SET retry_count = retry_count + 1,
			status = CASE WHEN retry_count + 1 >= $2 THEN 'failed_permanently' ELSE 'failed_retrying' END,
			next_retry_at = CASE WHEN retry_count + 1 >= $2 THEN NULL ELSE NOW() + (POWER(2, retry_count) * INTERVAL '1 minute') END,
			updated_at = NOW()
		WHERE id = $1 
		  AND status = 'processing'
		  AND status NOT IN ('delivered', 'failed_permanently')
		RETURNING status
	`, id, maxRetries).Scan(&newStatus)

	if errors.Is(err, pgx.ErrNoRows) {
		slog.WarnContext(ctx, "handleFailure: bildirim artik processing degil (muhtemelen iptal veya terminal durum)", "id", id)
		return false, nil
	}

	return newStatus == "failed_permanently", err
}
