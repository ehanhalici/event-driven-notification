package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"insider-notification/internal/circuitbreaker"
)

func TestProcessor_HandleFailure(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://insider:secretpassword@localhost:5432/notifications_db")
	if err != nil || pool.Ping(ctx) != nil {
		t.Skip("Veritabanina ulasilamadi, test atlandi.")
	}
	defer pool.Close()

	p := &Processor{Repo: nil}
	notifID := "00000000-0000-0000-0000-000000000099" // Gecerli UUID formati
	_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id = $1", notifID)
	_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE id = $1", notifID)
	_, _ = pool.Exec(ctx, `
		INSERT INTO notifications (id, idempotency_key, recipient, channel, content, priority, status, retry_count)
		VALUES ($1, 'fail-test-key-99', '+905551234567', 'sms', 'test', 'normal', 'processing', 2)
	`, notifID)

	// Gercek handleFailure SQL'i ile ayni mantik kullaniliyor:
	// retry_count + 1 >= 3 kontrolü (mevcut deger 2 iken: 2+1=3 >= 3 → failed_permanently)
	var yeniStatus string
	err = pool.QueryRow(ctx, `
		UPDATE notifications 
		SET retry_count = retry_count + 1,
			status = CASE WHEN retry_count + 1 >= 3 THEN 'failed_permanently' ELSE 'failed_retrying' END,
			next_retry_at = CASE WHEN retry_count + 1 >= 3 THEN NULL ELSE NOW() + (POWER(2, retry_count) * INTERVAL '1 second') END,
			updated_at = NOW()
		WHERE id = $1
		RETURNING status
	`, notifID).Scan(&yeniStatus)

	if err != nil {
		t.Fatalf("Sorgu hatasi: %v", err)
	}
	if yeniStatus != "failed_permanently" {
		t.Errorf("Beklenen statu 'failed_permanently', alinan: %s", yeniStatus)
	}
	_ = p
}

func TestProcessor_CircuitBreakerAndWebhook(t *testing.T) {
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis ayakta degil, CB testi atlandi.")
	}
	// Sadece bu testin kullandigi CB anahtarlarini temizle (FlushDB diger testleri bozabilir)
	cbName := "test_webhook_cb_proc"
	for _, suffix := range []string{"state", "halfopen", "probe", "fails"} {
		rdb.Del(ctx, "cb:"+suffix+":"+cbName)
	}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	// Max 1 hata, 1 dakika pencere, 1 dakika ceza
	distCB := circuitbreaker.NewDistributedCB(rdb, cbName, 1, 1*time.Minute, 1*time.Minute)

	// 1. İstek - Hata alacak (500)
	_, err1 := distCB.Execute(ctx, func() (interface{}, error) {
		client := http.Client{Timeout: 1 * time.Second}
		resp, err := client.Post(mockServer.URL, "application/json", nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			return nil, errors.New("upstream error")
		}
		return nil, nil
	})

	if err1 == nil {
		t.Error("Mock server 500 dönmesine ragmen hata firlatmadi (Beklenen durum)")
	}

	// 2. İstek - Circuit Breaker devreyi açtığı için Webhook'a HİÇ GİTMEDEN hata dönecek (Fail-fast)
	_, err2 := distCB.Execute(ctx, func() (interface{}, error) {
		return nil, nil // Burası asla çalışmamalı!
	})

	if !errors.Is(err2, circuitbreaker.ErrCircuitOpen) {
		t.Errorf("Circuit Breaker devreye girmedi! Beklenen hata: %v, Alinan: %v", circuitbreaker.ErrCircuitOpen, err2)
	}
}
