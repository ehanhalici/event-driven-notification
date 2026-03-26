package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"insider-notification/internal/models"
)

// Not: Bu testin çalışması için lokalde PostgreSQL ayakta olmalıdır.
func TestDB_CreateNotificationWithOutbox(t *testing.T) {
	ctx := context.Background()
	
	// Test veritabanı bağlantısı
	dbURL := "postgres://insider:secretpassword@localhost:5432/notifications_db"
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skip("Veritabanina baglanilamadi, test atlandi.")
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Skip("Veritabanina ulasilamadi, test atlandi.")
	}

	repo := &DB{Pool: pool}

	// 1. Başarılı Kayıt Senaryosu
	req := models.NotificationRequest{
		IdempotencyKey: "test-key-1",
		Recipient:      "+905550000000",
		Channel:        "sms",
		Content:        "Test Mesaji",
		Priority:       "high",
	}

	notifID := "test-uuid-1234"

	// Önce eski veriyi temizle (Idempotency çakışmaması için)
	_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id = $1", notifID)
	_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE id = $1", notifID)

	err = repo.CreateNotificationWithOutbox(ctx, req, notifID)
	if err != nil {
		t.Fatalf("Outbox ile kayit basarisiz oldu: %v", err)
	}

	// 2. Mükerrer Kayıt (Idempotency) Senaryosu
	// Aynı anahtarla tekrar yazmaya çalışalım
	reqDuplicate := req // IdempotencyKey aynı kaldı
	errDuplicate := repo.CreateNotificationWithOutbox(ctx, reqDuplicate, "test-uuid-5678")
	if errDuplicate == nil {
		t.Error("IdempotencyKey cakismasi yakalanamadi! Ikinci kayda izin verildi.")
	}
}

func TestDB_LockNotificationForProcessing(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://insider:secretpassword@localhost:5432/notifications_db")
	if err != nil || pool.Ping(ctx) != nil {
		t.Skip("Veritabanina ulasilamadi, test atlandi.")
	}
	defer pool.Close()

	repo := &DB{Pool: pool}
	notifID := "test-lock-uuid"

	// Hazırlık: Bekleyen bir kayıt at
	_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE id = $1", notifID)
	_, _ = pool.Exec(ctx, `
		INSERT INTO notifications (id, idempotency_key, recipient, channel, content, priority, status)
		VALUES ($1, 'lock-test-key', '123', 'sms', 'test', 'normal', 'pending')
	`, notifID)

	// 1. Worker kilit almayı dener
	locked, err := repo.LockNotificationForProcessing(ctx, notifID)
	if err != nil {
		t.Fatalf("Kilit alma sirasinda hata: %v", err)
	}
	if !locked {
		t.Error("Status 'pending' olmasina ragmen kilit alinamadi")
	}

	// 2. İkinci Worker aynı anda gelirse kilit alamaz (CAS Kontrolü)
	lockedAgain, err := repo.LockNotificationForProcessing(ctx, notifID)
	if err != nil {
		t.Fatalf("Ikinci kilit denemesinde hata: %v", err)
	}
	if lockedAgain {
		t.Error("Status 'processing' iken ikinci bir kilit alinmasina izin verildi! Race condition riski var.")
	}
}
