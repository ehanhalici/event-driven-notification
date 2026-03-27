package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"insider-notification/internal/models"
)

const testDBURL = "postgres://insider:secretpassword@localhost:5432/notifications_db"

// setupTestDB, test için veritabanı bağlantısı oluşturur. Bağlantı yoksa testi atlar.
func setupTestDB(t *testing.T) (*DB, *pgxpool.Pool) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDBURL)
	if err != nil {
		t.Skip("Veritabanina baglanilamadi, test atlandi.")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skip("Veritabanina ulasilamadi, test atlandi.")
	}
	return &DB{Pool: pool}, pool
}

// insertTestNotification, test için bir bildirim kaydı ekler ve temizlik fonksiyonu döndürür.
func insertTestNotification(t *testing.T, pool *pgxpool.Pool, id, status string) func() {
	ctx := context.Background()
	idempKey := "test-idem-" + uuid.New().String()
	_, err := pool.Exec(ctx, `
		INSERT INTO notifications (id, idempotency_key, recipient, channel, content, priority, status)
		VALUES ($1, $2, '+905551234567', 'sms', 'test content', 'normal', $3)
	`, id, idempKey, status)
	if err != nil {
		t.Fatalf("Test verisi eklenemedi: %v", err)
	}
	return func() {
		_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id = $1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE id = $1", id)
	}
}

// =====================================================================
// CreateNotificationWithOutbox TESTLERİ
// =====================================================================

func TestDB_CreateNotificationWithOutbox(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	idempKey := "test-create-" + uuid.New().String()

	// Temizlik
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id = $1", notifID)
		_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE id = $1", notifID)
	}()

	req := models.NotificationRequest{
		IdempotencyKey: idempKey,
		Recipient:      "+905550000000",
		Channel:        "sms",
		Content:        "Test Mesaji",
		Priority:       "high",
	}

	// 1. Başarılı kayıt
	err := repo.CreateNotificationWithOutbox(ctx, req, notifID)
	if err != nil {
		t.Fatalf("Outbox ile kayit basarisiz oldu: %v", err)
	}

	// Kayıt doğrulama
	var savedStatus string
	err = pool.QueryRow(ctx, "SELECT status FROM notifications WHERE id = $1", notifID).Scan(&savedStatus)
	if err != nil {
		t.Fatalf("Kayit okunamadi: %v", err)
	}
	if savedStatus != "pending" {
		t.Errorf("Beklenen status 'pending', alinan: '%s'", savedStatus)
	}

	// Outbox kayıt doğrulama
	var outboxCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = $1", notifID).Scan(&outboxCount)
	if err != nil {
		t.Fatalf("Outbox sorgusu basarisiz: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("Outbox'ta 1 kayit bekleniyor, alinan: %d", outboxCount)
	}

	// 2. Mükerrer kayıt (Idempotency)
	reqDuplicate := req
	errDuplicate := repo.CreateNotificationWithOutbox(ctx, reqDuplicate, uuid.New().String())
	if errDuplicate == nil {
		t.Error("IdempotencyKey cakismasi yakalanamadi! Ikinci kayda izin verildi.")
	}
}

// =====================================================================
// LockNotificationForProcessing TESTLERİ
// =====================================================================

func TestDB_LockNotificationForProcessing(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "pending")
	defer cleanup()

	// 1. Worker kilit alır
	locked, err := repo.LockNotificationForProcessing(ctx, notifID)
	if err != nil {
		t.Fatalf("Kilit alma sirasinda hata: %v", err)
	}
	if !locked {
		t.Error("Status 'pending' olmasina ragmen kilit alinamadi")
	}

	// 2. İkinci Worker kilit alamaz (CAS)
	lockedAgain, err := repo.LockNotificationForProcessing(ctx, notifID)
	if err != nil {
		t.Fatalf("Ikinci kilit denemesinde hata: %v", err)
	}
	if lockedAgain {
		t.Error("Status 'processing' iken ikinci kilit alinmasina izin verildi!")
	}
}

func TestDB_LockNotificationForProcessing_DeliveredNotLockable(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "delivered")
	defer cleanup()

	locked, err := repo.LockNotificationForProcessing(ctx, notifID)
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if locked {
		t.Error("'delivered' durumundaki bildirim kilitlenmemeli")
	}
}

func TestDB_LockNotificationForProcessing_NonExistent(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	locked, err := repo.LockNotificationForProcessing(ctx, uuid.New().String())
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if locked {
		t.Error("Var olmayan bildirim kilitlenmemeli")
	}
}

// =====================================================================
// CancelNotification TESTLERİ
// =====================================================================

func TestDB_CancelNotification_Pending(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "pending")
	defer cleanup()

	canceled, currentStatus, err := repo.CancelNotification(ctx, notifID)
	if err != nil {
		t.Fatalf("Iptal hatasi: %v", err)
	}
	if !canceled {
		t.Error("Pending bildirim iptal edilebilmeli")
	}
	if currentStatus != "canceled" {
		t.Errorf("Beklenen status 'canceled', alinan: '%s'", currentStatus)
	}
}

func TestDB_CancelNotification_FailedRetrying(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "failed_retrying")
	defer cleanup()

	canceled, currentStatus, err := repo.CancelNotification(ctx, notifID)
	if err != nil {
		t.Fatalf("Iptal hatasi: %v", err)
	}
	if !canceled {
		t.Error("failed_retrying bildirim iptal edilebilmeli")
	}
	if currentStatus != "canceled" {
		t.Errorf("Beklenen status 'canceled', alinan: '%s'", currentStatus)
	}
}

func TestDB_CancelNotification_Processing(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "processing")
	defer cleanup()

	canceled, _, err := repo.CancelNotification(ctx, notifID)
	if err != nil {
		t.Fatalf("Iptal hatasi: %v", err)
	}
	if !canceled {
		t.Error("processing bildirim iptal edilebilmeli")
	}
}

func TestDB_CancelNotification_Delivered_NotCancelable(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "delivered")
	defer cleanup()

	canceled, currentStatus, err := repo.CancelNotification(ctx, notifID)
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if canceled {
		t.Error("delivered bildirim iptal edilememeli")
	}
	if currentStatus != "delivered" {
		t.Errorf("Mevcut status 'delivered' donmeli, alinan: '%s'", currentStatus)
	}
}

func TestDB_CancelNotification_FailedPermanently_NotCancelable(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "failed_permanently")
	defer cleanup()

	canceled, currentStatus, err := repo.CancelNotification(ctx, notifID)
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if canceled {
		t.Error("failed_permanently bildirim iptal edilememeli")
	}
	if currentStatus != "failed_permanently" {
		t.Errorf("Mevcut status 'failed_permanently' donmeli, alinan: '%s'", currentStatus)
	}
}

func TestDB_CancelNotification_NonExistent(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	canceled, currentStatus, err := repo.CancelNotification(ctx, uuid.New().String())
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if canceled {
		t.Error("Var olmayan bildirim iptal edilememeli")
	}
	if currentStatus != "" {
		t.Errorf("Var olmayan bildirim icin status bos donmeli, alinan: '%s'", currentStatus)
	}
}

func TestDB_CancelNotification_AlreadyCanceled(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "canceled")
	defer cleanup()

	canceled, currentStatus, err := repo.CancelNotification(ctx, notifID)
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if canceled {
		t.Error("Zaten iptal edilmis bildirim tekrar iptal edilememeli")
	}
	if currentStatus != "canceled" {
		t.Errorf("Mevcut status 'canceled' donmeli, alinan: '%s'", currentStatus)
	}
}

// =====================================================================
// GetNotificationByID TESTLERİ
// =====================================================================

func TestDB_GetNotificationByID_Exists(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "pending")
	defer cleanup()

	notif, err := repo.GetNotificationByID(ctx, notifID)
	if err != nil {
		t.Fatalf("Bildirim getirme hatasi: %v", err)
	}
	if notif == nil {
		t.Fatal("Bildirim nil donmemeli")
	}
	if notif.ID != notifID {
		t.Errorf("Beklenen ID '%s', alinan: '%s'", notifID, notif.ID)
	}
	if notif.Status != "pending" {
		t.Errorf("Beklenen status 'pending', alinan: '%s'", notif.Status)
	}
	if notif.Channel != "sms" {
		t.Errorf("Beklenen channel 'sms', alinan: '%s'", notif.Channel)
	}
	if notif.Recipient != "+905551234567" {
		t.Errorf("Beklenen recipient '+905551234567', alinan: '%s'", notif.Recipient)
	}
}

func TestDB_GetNotificationByID_NotFound(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	_, err := repo.GetNotificationByID(ctx, uuid.New().String())
	if err == nil {
		t.Error("Var olmayan bildirim icin hata bekleniyor")
	}
}

// =====================================================================
// GetNotificationsByBatchID TESTLERİ
// =====================================================================

func TestDB_GetNotificationsByBatchID_EmptyBatch(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	list, err := repo.GetNotificationsByBatchID(ctx, uuid.New().String())
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("Bos batch icin 0 sonuc bekleniyor, alinan: %d", len(list))
	}
}

func TestDB_GetNotificationsByBatchID_WithData(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	batchID := uuid.New().String()
	notifID1 := uuid.New().String()
	notifID2 := uuid.New().String()
	idempKey1 := "batch-test-" + uuid.New().String()
	idempKey2 := "batch-test-" + uuid.New().String()

	// Temizlik
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id IN ($1, $2)", notifID1, notifID2)
		_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE id IN ($1, $2)", notifID1, notifID2)
	}()

	// Batch ile 2 bildirim ekle
	_, err := pool.Exec(ctx, `
		INSERT INTO notifications (id, batch_id, idempotency_key, recipient, channel, content, priority, status)
		VALUES ($1, $2, $3, '+905551234567', 'sms', 'batch test 1', 'normal', 'pending')
	`, notifID1, batchID, idempKey1)
	if err != nil {
		t.Fatalf("Veri ekleme hatasi: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO notifications (id, batch_id, idempotency_key, recipient, channel, content, priority, status)
		VALUES ($1, $2, $3, 'test@example.com', 'email', 'batch test 2', 'high', 'pending')
	`, notifID2, batchID, idempKey2)
	if err != nil {
		t.Fatalf("Veri ekleme hatasi: %v", err)
	}

	list, err := repo.GetNotificationsByBatchID(ctx, batchID)
	if err != nil {
		t.Fatalf("Batch sorgu hatasi: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("Batch icin 2 sonuc bekleniyor, alinan: %d", len(list))
	}
}

// =====================================================================
// CreateNotificationBatchWithOutbox TESTLERİ
// =====================================================================

func TestDB_CreateNotificationBatchWithOutbox(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	batchID := uuid.New().String()

	reqs := []models.NotificationRequest{
		{
			IdempotencyKey: "batch-create-" + uuid.New().String(),
			Recipient:      "+905551111111",
			Channel:        "sms",
			Content:        "Batch mesaj 1",
			Priority:       "high",
		},
		{
			IdempotencyKey: "batch-create-" + uuid.New().String(),
			Recipient:      "test@example.com",
			Channel:        "email",
			Content:        "Batch mesaj 2",
			Priority:       "normal",
		},
	}

	err := repo.CreateNotificationBatchWithOutbox(ctx, reqs, batchID)
	if err != nil {
		t.Fatalf("Batch olusturma hatasi: %v", err)
	}

	// Temizlik
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM notifications WHERE batch_id = $1)", batchID)
		_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE batch_id = $1", batchID)
	}()

	// Doğrulama
	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM notifications WHERE batch_id = $1", batchID).Scan(&count)
	if err != nil {
		t.Fatalf("Sorgu hatasi: %v", err)
	}
	if count != 2 {
		t.Errorf("Beklenen 2 kayit, alinan: %d", count)
	}

	// Outbox doğrulama
	var outboxCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox_events WHERE aggregate_id IN (SELECT id FROM notifications WHERE batch_id = $1)", batchID).Scan(&outboxCount)
	if err != nil {
		t.Fatalf("Outbox sorgu hatasi: %v", err)
	}
	if outboxCount != 2 {
		t.Errorf("Outbox'ta 2 kayit bekleniyor, alinan: %d", outboxCount)
	}
}

func TestDB_CreateNotificationBatchWithOutbox_DuplicateIdempotencyKey(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	batchID := uuid.New().String()
	sharedKey := "batch-dup-" + uuid.New().String()

	reqs := []models.NotificationRequest{
		{
			IdempotencyKey: sharedKey,
			Recipient:      "+905551111111",
			Channel:        "sms",
			Content:        "Mesaj 1",
			Priority:       "normal",
		},
		{
			IdempotencyKey: sharedKey, // Aynı idempotency key!
			Recipient:      "+905552222222",
			Channel:        "sms",
			Content:        "Mesaj 2",
			Priority:       "normal",
		},
	}

	err := repo.CreateNotificationBatchWithOutbox(ctx, reqs, batchID)
	if err == nil {
		// Temizlik
		_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM notifications WHERE batch_id = $1)", batchID)
		_, _ = pool.Exec(ctx, "DELETE FROM notifications WHERE batch_id = $1", batchID)
		t.Error("Ayni idempotency_key ile batch kayit hataya donmeli")
	}
}

// =====================================================================
// ListNotifications TESTLERİ
// =====================================================================

func TestDB_ListNotifications_NoFilter(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()

	// Filtresiz listeleme (en az 0 sonuç gelecek)
	list, nextCursor, err := repo.ListNotifications(ctx, "", "", nil, nil, nil, "", 10)
	if err != nil {
		t.Fatalf("Listeleme hatasi: %v", err)
	}

	// Hata olmamalı
	if list == nil {
		t.Error("list nil olmamali (bos dilim olabilir)")
	}

	// nextCursor boş veya dolu olabilir, hata olmadığını doğruluyoruz
	_ = nextCursor
}

func TestDB_ListNotifications_StatusFilter(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	notifID := uuid.New().String()
	cleanup := insertTestNotification(t, pool, notifID, "delivered")
	defer cleanup()

	list, _, err := repo.ListNotifications(ctx, "delivered", "", nil, nil, nil, "", 100)
	if err != nil {
		t.Fatalf("Listeleme hatasi: %v", err)
	}

	found := false
	for _, n := range list {
		if n.ID == notifID {
			found = true
			break
		}
	}
	if !found {
		t.Error("delivered filtresi ile eklenen bildirim listede bulunamadi")
	}
}

func TestDB_ListNotifications_ChannelFilter(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()

	// Sadece email kanalını filtrele
	list, _, err := repo.ListNotifications(ctx, "", "email", nil, nil, nil, "", 10)
	if err != nil {
		t.Fatalf("Listeleme hatasi: %v", err)
	}

	for _, n := range list {
		if n.Channel != "email" {
			t.Errorf("Kanal filtresi calismadi: beklenen 'email', alinan '%s'", n.Channel)
		}
	}
}

func TestDB_ListNotifications_LimitWorks(t *testing.T) {
	repo, pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()

	// Limit 2 ile çağır
	list, _, err := repo.ListNotifications(ctx, "", "", nil, nil, nil, "", 2)
	if err != nil {
		t.Fatalf("Listeleme hatasi: %v", err)
	}

	if len(list) > 2 {
		t.Errorf("Limit 2 iken %d sonuc dondu", len(list))
	}
}
