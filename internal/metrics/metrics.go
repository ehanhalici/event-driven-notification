// internal/metrics/metrics.go
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// =========================================================================
	// 1. API METRİKLERİ
	// =========================================================================

	// APIRequestsTotal, HTTP isteklerinin toplam sayısını method, route ve HTTP status_code'a göre izler.
	// HighCardinality koruması: route parametreleri "/notifications/{id}" gibi statik pattern'ler olmalıdır.
	APIRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_http_requests_total",
			Help: "Toplam HTTP istek sayisi (method, route, status_code)",
		},
		[]string{"method", "route", "status_code"},
	)

	// APIRequestDuration, API endpoint'lerinin yanıt verme süresini (latency) ölçer.
	APIRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "api_request_duration_seconds",
			Help:    "API endpoint'lerinin yanit verme suresi (Latency)",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"method", "route"},
	)

	// NotificationsCreatedTotal, API aracılığıyla oluşturulan bildirim sayısını kanal bazında sayar.
	NotificationsCreatedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "notification_created_total",
			Help: "API ile olusturulan toplam bildirim sayisi (channel)",
		},
		[]string{"channel"},
	)

	// NotificationBatchSize, toplu bildirimlerin boyut dağılımını (histogram) izler.
	NotificationBatchSize = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "notification_batch_size",
			Help:    "Toplu bildirim isteklerindeki bildirim adedi dagilimi",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000},
		},
	)

	// NotificationsCanceledTotal, iptal edilen bildirim sayısını izler.
	NotificationsCanceledTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "notification_canceled_total",
			Help: "Iptal edilen toplam bildirim sayisi",
		},
	)

	// =========================================================================
	// 2. RATE LIMIT METRİKLERİ
	// =========================================================================

	// RateLimitHitsTotal, rate limit tarafından reddedilen isteklerin sayısını izler.
	RateLimitHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rate_limit_hits_total",
			Help: "Rate limiter tarafindan reddedilen istek sayisi (limiter_type)",
		},
		[]string{"limiter_type"}, // "global_flood", "token_bucket", "channel_sliding"
	)

	// =========================================================================
	// 3. İŞLEME MOTORU (Worker) METRİKLERİ
	// =========================================================================

	// EventsProcessed, işlenen toplam bildirim sayısını durum ve kanala göre izler.
	EventsProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "notification_events_processed_total",
			Help: "Islenen toplam bildirim sayisi (status ve channel)",
		},
		[]string{"status", "channel"}, // delivered, failed_permanently, retrying
	)

	// EventProcessingDuration, bildirimlerin uçtan uca işlenme süresini ölçer.
	EventProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "notification_processing_duration_seconds",
			Help:    "Bildirimlerin toplam islenme suresi (Latency)",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"channel"},
	)

	// WebhookCallDuration, dış webhook servisine yapılan HTTP çağrılarının süresini ölçer.
	WebhookCallDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "webhook_call_duration_seconds",
			Help:    "Dis webhook servisine yapilan HTTP cagrilarinin suresi",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"channel"},
	)

	// WebhookCallsTotal, webhook çağrılarının sonuçlarını izler.
	WebhookCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "webhook_calls_total",
			Help: "Webhook cagrilarinin toplam sayisi (channel, result)",
		},
		[]string{"channel", "result"}, // result: "success", "error", "circuit_open"
	)

	// RetryTotal, yeniden deneme (retry) yapılan bildirim sayısını izler.
	RetryTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "notification_retry_total",
			Help: "Yeniden denenen bildirimlerin toplam sayisi (channel)",
		},
		[]string{"channel"},
	)

	// ActiveWorkers, o anda aktif olarak mesaj işleyen worker sayısını gösterir.
	ActiveWorkers = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "notification_active_workers",
			Help: "O anda aktif mesaj isleyen worker sayisi (topic)",
		},
		[]string{"topic"},
	)

	// =========================================================================
	// 4. KAFKA METRİKLERİ
	// =========================================================================

	// KafkaMessagesConsumedTotal, Kafka'dan tüketilen mesajların toplam sayısını izler.
	KafkaMessagesConsumedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kafka_messages_consumed_total",
			Help: "Kafka'dan tuketilen toplam mesaj sayisi (topic)",
		},
		[]string{"topic"},
	)

	// KafkaDLQMessagesTotal, Dead Letter Queue'ya gönderilen zehirli mesaj sayısı.
	KafkaDLQMessagesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "kafka_dlq_messages_total",
			Help: "Dead Letter Queue'ya gonderilen toplam mesaj sayisi",
		},
	)

	// InFlightMessages, şu anda worker pipeline'ında işlenmekte olan mesaj sayısı.
	InFlightMessages = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "notification_inflight_messages",
			Help: "Worker pipeline'inda su anda islenmekte olan mesaj sayisi (topic)",
		},
		[]string{"topic"},
	)

	// =========================================================================
	// 5. OUTBOX & RELAY METRİKLERİ
	// =========================================================================

	// OutboxQueueDepth, Outbox tablosunda bekleyen mesaj sayısını gösterir.
	OutboxQueueDepth = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "notification_outbox_queue_depth",
			Help: "Outbox tablosunda bekleyen (pending) mesaj sayisi",
		},
	)

	// OutboxRelayedTotal, Relay tarafından Kafka'ya başarıyla iletilen mesaj sayısı.
	OutboxRelayedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "outbox_relayed_total",
			Help: "Relay tarafindan Kafka'ya basariyla iletilen toplam mesaj sayisi",
		},
	)

	// ZombieRecoveredTotal, stuck (zombi) durumdan kurtarılan bildirim sayısı.
	ZombieRecoveredTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "notification_zombie_recovered_total",
			Help: "Stuck (zombi) durumdan kurtarilan toplam bildirim sayisi",
		},
	)

	// =========================================================================
	// 6. CIRCUIT BREAKER METRİKLERİ
	// =========================================================================

	// CircuitBreakerState, devre kesicinin anlık durumunu gösterir.
	// 0 = closed (normal), 1 = open (engel), 2 = half-open (test)
	CircuitBreakerState = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "circuit_breaker_state",
			Help: "Devre kesicinin anlık durumu (0=closed, 1=open, 2=half_open)",
		},
	)

	// CircuitBreakerTripsTotal, devre kesicinin açılma (trip) sayısını izler.
	CircuitBreakerTripsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "circuit_breaker_trips_total",
			Help: "Devre kesicinin toplam acilma (trip) sayisi",
		},
	)
)
