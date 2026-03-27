package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEventsProcessed_IsRegistered(t *testing.T) {
	// EventsProcessed metric'i promauto ile otomatik kaydedilir.
	counter := EventsProcessed.WithLabelValues("test_registered", "test_channel")
	if counter == nil {
		t.Error("EventsProcessed counter nil olmamali")
	}
}

func TestEventsProcessed_IncrementWorks(t *testing.T) {
	counter := EventsProcessed.WithLabelValues("test_increment_metric", "test_ch")
	counter.Inc()
	counter.Inc()

	val := testutil.ToFloat64(counter)
	if val < 2 {
		t.Errorf("Sayac en az 2 olmali, alinan: %f", val)
	}
}

func TestEventProcessingDuration_IsRegistered(t *testing.T) {
	observer := EventProcessingDuration.WithLabelValues("test_latency_ch")
	if observer == nil {
		t.Error("EventProcessingDuration observer nil olmamali")
	}
	// Histogram'a bir değer gözlemleyebildiğimizi doğrula (panic olmamalı)
	observer.Observe(0.5)
	observer.Observe(1.5)
}

func TestOutboxQueueDepth_SetAndRead(t *testing.T) {
	OutboxQueueDepth.Set(42)
	val := testutil.ToFloat64(OutboxQueueDepth)
	if val != 42 {
		t.Errorf("OutboxQueueDepth 42 olmali, alinan: %f", val)
	}
}

func TestOutboxQueueDepth_IncDecWorks(t *testing.T) {
	OutboxQueueDepth.Set(0)
	OutboxQueueDepth.Inc()
	OutboxQueueDepth.Inc()
	OutboxQueueDepth.Dec()

	val := testutil.ToFloat64(OutboxQueueDepth)
	if val != 1 {
		t.Errorf("Beklenen 1, alinan: %f", val)
	}
}

func TestAPIRequestDuration_IsRegistered(t *testing.T) {
	observer := APIRequestDuration.WithLabelValues("GET", "/test_metrics")
	if observer == nil {
		t.Error("APIRequestDuration observer nil olmamali")
	}
	observer.Observe(0.01)
}

func TestAPIRequestDuration_MultipleLabels(t *testing.T) {
	// Farklı method ve route kombinasyonları çalışmalı
	methods := []string{"GET", "POST", "DELETE"}
	routes := []string{"/test_m1", "/test_m2", "/test_m3"}

	for _, method := range methods {
		for _, route := range routes {
			observer := APIRequestDuration.WithLabelValues(method, route)
			if observer == nil {
				t.Errorf("APIRequestDuration nil olmamali: method=%s, route=%s", method, route)
			}
			observer.Observe(0.1)
		}
	}
}

func TestEventsProcessed_DescribeCollect(t *testing.T) {
	// Describe ve Collect fonksiyonlarının çalıştığını doğrula
	descCh := make(chan *prometheus.Desc, 10)
	EventsProcessed.Describe(descCh)
	close(descCh)

	descCount := 0
	for range descCh {
		descCount++
	}
	if descCount == 0 {
		t.Error("EventsProcessed en az 1 desc döndürmeli")
	}
}
