package repository

import (
	"fmt"
	"context"
	"encoding/json"
	"errors"
	"time"
	"encoding/base64"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5" // ErrNoRows icin gerekli
	"github.com/jackc/pgx/v5/pgxpool"
	"insider-notification/internal/models"
	"insider-notification/internal/logger"
)

type DB struct {
	Pool *pgxpool.Pool
}

// CreateNotificationWithOutbox, API'den gelen isteği Outbox pattern ile kaydeder.
func (db *DB) CreateNotificationWithOutbox(ctx context.Context, req models.NotificationRequest, notifID string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	corrID, _ := ctx.Value(logger.CorrelationIDKey).(string)
	// 1. Ana tabloya yaz
	_, err = tx.Exec(ctx, `
        INSERT INTO notifications 
        (id, idempotency_key, recipient, channel, content, priority, status, correlation_id)
        VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7)
	`, notifID, req.IdempotencyKey, req.Recipient, req.Channel, req.Content, req.Priority, corrID)
	if err != nil {
		return err // Idempotency key çakışması burada pgconn.PgError olarak döner
	}

	//  Outbox'a Req değil, ID içeren Payload yazılıyor # todo buradaki karmasayi duzelt
	internalPayload := models.NotificationPayload{
		ID:       notifID,
		CorrelationID: corrID,
		Recipient: req.Recipient,
		Channel:  req.Channel,
		Content:  req.Content,
		Priority: req.Priority,
	}
	
	payload, err := json.Marshal(internalPayload)
	if err != nil {
		return err
	}

	// 2. Outbox tablosuna yaz
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (aggregate_id, event_type, payload)
		VALUES ($1, 'NotificationCreated', $2)
	`, notifID, payload)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (db *DB) LockNotificationForProcessing(ctx context.Context, id string) (bool, error) {
	var lockedID string
	err := db.Pool.QueryRow(ctx, `
		UPDATE notifications 
		SET status = 'processing', updated_at = NOW() 
		WHERE id = $1 
		  AND (
		      status = 'pending' 
		      OR (status = 'failed_retrying' AND next_retry_at <= NOW())
		  )
		RETURNING id
	`, id).Scan(&lockedID)

	if err != nil {
		//  Satır bulunamadıysa (başka worker aldı veya iptal edildi) false dön
		if errors.Is(err, pgx.ErrNoRows) {
			// Eğer zamanı gelmediyse (next_retry_at > NOW) 0 satır güncellenir ve buraya düşer.
			return false, nil
		}
		return false, err
	}
	return true, nil
}


// 3 - Toplu Bildirim Oluşturma (Batch Creation)
func (db *DB) CreateNotificationBatchWithOutbox(ctx context.Context, requests []models.NotificationRequest, batchID string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// pgx.Batch nesnesini oluştur
	batch := &pgx.Batch{}

	for _, req := range requests {
		notifID := uuid.New().String()
		corrID, _ := ctx.Value(logger.CorrelationIDKey).(string)
		// 1. Ana tablo sorgusunu kuyruğa al
		batch.Queue(`
            INSERT INTO notifications (
                id, 
                batch_id, 
                idempotency_key, 
                recipient, 
                channel, 
                content, 
                priority, 
                status, 
                correlation_id
            ) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8)
        `, notifID, batchID, req.IdempotencyKey, req.Recipient, req.Channel, req.Content, req.Priority, corrID)
		// Outbox payload'unu hazırla
		internalPayload := models.NotificationPayload{
			ID:       notifID,
			CorrelationID: corrID,
			Recipient: req.Recipient,
			Channel:  req.Channel,
			Content:  req.Content,
			Priority: req.Priority,
		}
		payloadBytes, err := json.Marshal(internalPayload)
		if err != nil{
			return fmt.Errorf("payload marshal error for %s: %w", notifID, err)
		}

		// 2. Outbox sorgusunu kuyruğa al
		batch.Queue(`
			INSERT INTO outbox_events (aggregate_id, event_type, payload)
			VALUES ($1, 'NotificationCreated', $2)
		`, notifID, payloadBytes)
	}

	// Tüm kuyruğu TEK SEFERDE veritabanına gönder (Round-trip optimizasyonu)
	br := tx.SendBatch(ctx, batch)
	// ---> [KRİTİK DÜZELTME]: Güvenlik için defer ile kapatmayı garantiye al
	defer br.Close() 

	// ---> [KRİTİK DÜZELTME]: pgx Sürücü İterasyonu <---
	// Gönderdiğimiz her bir sorgunun sonucunu ve hatasını tek tek okumalıyız.
	// Bu sayede 23505 (Unique Violation) hatası kaybolmadan API katmanına kadar ulaşır!
	for i := 0; i < batch.Len(); i++ {
		_, err := br.Exec()
		if err != nil {
			return err // Orijinal hatayı fırlat (Idempotency burada yakalanacak)
		}
	}
	err = br.Close()
    if err != nil {
        return fmt.Errorf("batch close error: %w", err)
    }

	return tx.Commit(ctx)	

}

// 2. Batch ID ile bildirimleri getirme
func (db *DB) GetNotificationsByBatchID(ctx context.Context, batchID string) ([]models.NotificationResponse, error) {
	query := `
		SELECT id, batch_id, external_id, idempotency_key, recipient, channel, content, priority, status, retry_count, next_retry_at, created_at, updated_at 
		FROM notifications WHERE batch_id = $1 ORDER BY created_at DESC
	`
	rows, err := db.Pool.Query(ctx, query, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.NotificationResponse
	for rows.Next() {
		var n models.NotificationResponse
		if err := rows.Scan(&n.ID, &n.BatchID, &n.ExternalID, &n.IdempotencyKey, &n.Recipient, &n.Channel, &n.Content, &n.Priority, &n.Status, &n.RetryCount, &n.NextRetryAt, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		results = append(results, n)
	}
	return results, nil
}


// İptal İşlemi (Sadece pending durumundakiler iptal edilebilir)
func (db *DB) CancelNotification(ctx context.Context, id string) (bool, error) {
	tag, err := db.Pool.Exec(ctx, `
		UPDATE notifications 
		SET status = 'canceled', updated_at = NOW() 
		WHERE id = $1 AND (status = 'pending' OR status = 'failed_retrying')
	`, id)
	if err != nil {
		return false, err
	}
	// Eğer güncellenen satır sayısı 0 ise, ya mesaj yoktur ya da artık pending değildir
	return tag.RowsAffected() > 0, nil
}

// Tekil Bildirim Getirme
func (db *DB) GetNotificationByID(ctx context.Context, id string) (*models.NotificationResponse, error) {
	var n models.NotificationResponse
	err := db.Pool.QueryRow(ctx, `
		SELECT id, batch_id, external_id, idempotency_key, recipient, channel, content, priority, status, retry_count, next_retry_at, created_at, updated_at 
		FROM notifications WHERE id = $1
	`, id).Scan(
		&n.ID, &n.BatchID, &n.ExternalID, &n.IdempotencyKey, &n.Recipient, 
		&n.Channel, &n.Content, &n.Priority, &n.Status, &n.RetryCount, 
		&n.NextRetryAt, &n.CreatedAt, &n.UpdatedAt,
	)
	
	if err != nil {
		return nil, err // Kayıt yoksa pgx.ErrNoRows döner
	}
	return &n, nil
}

// Dinamik Filtreli Listeleme
func (db *DB) ListNotifications(ctx context.Context, status, channel string, startDate, endDate *time.Time, cursorTime *time.Time, cursorID string, limit int) ([]models.NotificationResponse, string, error) {
	query := `SELECT id, batch_id, external_id, idempotency_key, recipient, channel, content, priority, status, retry_count, next_retry_at, created_at, updated_at FROM notifications WHERE 1=1`
	args := []interface{}{}
	argID := 1

	if status != "" {
		query += fmt.Sprintf(` AND status = $%d`, argID)
		args = append(args, status)
		argID++
	}
	if channel != "" {
		query += fmt.Sprintf(` AND channel = $%d`, argID)
		args = append(args, channel)
		argID++
	}
	if startDate != nil {
		query += fmt.Sprintf(` AND created_at >= $%d`, argID)
		args = append(args, *startDate)
		argID++
	}
	if endDate != nil {
		query += fmt.Sprintf(` AND created_at <= $%d`, argID)
		args = append(args, *endDate)
		argID++
	}

	// ---> [YENİ]: KEYSET PAGINATION MANTIĞI <---
	// Eğer kullanıcı bir sayfa imleci (cursor) gönderdiyse, o kayıttan "daha eski" olanları getir.
	// Postgres'in mükemmel Tuple Comparison (Satır Karşılaştırması) özelliği ile:
	if cursorTime != nil && cursorID != "" {
		query += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, argID, argID+1)
		args = append(args, *cursorTime, cursorID)
		argID += 2
	}

	// Sıralama kararlı olmalıdır. created_at aynı olanlarda id baz alınarak çakışma (tie) çözülür.
	// OFFSET silindi!
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, argID)
	args = append(args, limit)

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var results []models.NotificationResponse
	for rows.Next() {
		var n models.NotificationResponse
		if err := rows.Scan(&n.ID, &n.BatchID, &n.ExternalID, &n.IdempotencyKey, &n.Recipient, &n.Channel, &n.Content, &n.Priority, &n.Status, &n.RetryCount, &n.NextRetryAt, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, "", fmt.Errorf("scan error: %w", err)
		}
		results = append(results, n)
	}

	// ---> [YENİ]: SONRAKİ SAYFANIN İMLECİNİ (CURSOR) OLUŞTUR <---
	var nextCursor string
	if len(results) == limit {
		// Limit kadar veri geldiyse, muhtemelen bir sonraki sayfa var.
		// Listenin en sonundaki (en eski) elemanın verilerini alıp şifreliyoruz.
		lastItem := results[len(results)-1]
		// Format: "2026-03-25T14:30:00.123456Z|123e4567-e89b-12d3-a456-426614174000"
		rawCursor := fmt.Sprintf("%s|%s", lastItem.CreatedAt.Format(time.RFC3339Nano), lastItem.ID)
		nextCursor = base64.StdEncoding.EncodeToString([]byte(rawCursor))
	}

	return results, nextCursor, nil
}
