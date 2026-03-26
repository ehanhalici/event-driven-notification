package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/redis/go-redis/v9"
	"insider-notification/internal/ratelimit"
)

// Ortak test handler'ı oluşturan yardımcı fonksiyon
func setupTestHandler(t *testing.T) (*Handler, *redis.Client) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skip("Redis ayakta degil, API testleri atlandi.")
	}
	
	h := &Handler{
		Repo:    nil, // Veritabanı mock'lanıyor
		Redis:   rdb,
		Limiter: ratelimit.NewLimiter(rdb),
	}
	return h, rdb
}

func TestHandleCreateNotification_InvalidJSON(t *testing.T) {
	h, rdb := setupTestHandler(t)
	if rdb != nil { defer rdb.Close() }

	invalidJSON := []byte(`{"idempotency_key": "123", "unknown_field": "test"}`)
	req, _ := http.NewRequest("POST", "/notifications", bytes.NewBuffer(invalidJSON))
	rr := httptest.NewRecorder()

	h.HandleCreateNotification(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("Beklenen HTTP durumu %v, ama %v alindi", http.StatusBadRequest, status)
	}
}

func TestHandleCreateNotificationBatch_EmptyList(t *testing.T) {
	h, rdb := setupTestHandler(t)
	if rdb != nil { defer rdb.Close() }

	emptyListJSON := []byte(`[]`)
	req, _ := http.NewRequest("POST", "/notifications/batch", bytes.NewBuffer(emptyListJSON))
	rr := httptest.NewRecorder()

	h.HandleCreateNotificationBatch(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("Beklenen HTTP durumu %v, ama %v alindi", http.StatusBadRequest, status)
	}
}

func TestHandleCreateNotificationBatch_ExceedsLimit(t *testing.T) {
	h, rdb := setupTestHandler(t)
	if rdb != nil { defer rdb.Close() }

	var bigArray bytes.Buffer
	bigArray.WriteString(`[`)
	for i := 0; i < 1001; i++ {
		bigArray.WriteString(`{"idempotency_key": "key", "recipient": "+905551234567", "channel": "sms", "content": "test", "priority": "low"}`)
		if i < 1000 {
			bigArray.WriteString(`,`)
		}
	}
	bigArray.WriteString(`]`)

	req, _ := http.NewRequest("POST", "/notifications/batch", &bigArray)
	rr := httptest.NewRecorder()

	h.HandleCreateNotificationBatch(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("Beklenen HTTP durumu %v, ama %v alindi", http.StatusBadRequest, status)
	}
}
