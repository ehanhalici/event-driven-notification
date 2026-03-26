// internal/metrics/metrics.go
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Success/Failure Rates (Başarı ve Hata Oranları)
	EventsProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "notification_events_processed_total",
			Help: "İşlenen toplam bildirim sayısı (durum ve kanala göre)",
		},
		[]string{"status", "channel"}, // Label'lar: delivered, failed vs.
	)

	// Latency (İşlenme Gecikmesi)
	EventProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "notification_processing_duration_seconds",
			Help:    "Bildirimlerin işlenme süresi (Latency)",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"channel"},
	)

	// Queue Depth (Kuyruk Derinliği / Outbox Şişkinliği)
	OutboxQueueDepth = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "notification_outbox_queue_depth",
			Help: "Outbox tablosunda bekleyen (pending) mesaj sayısı",
		},
	)

	// API Latency (API İstek Gecikmeleri)
	APIRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "api_request_duration_seconds",
			Help:    "API endpoint'lerinin yanit verme suresi (Latency)",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"method", "route"}, // route parametresi "/notifications/{id}" gibi statik kalmali!
	)
)
