// internal/models/types.go
package models

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"regexp"
)

// API'den gelen istek
type NotificationRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Recipient      string `json:"recipient"`
	Channel        string `json:"channel"`
	Content        string `json:"content"`
	Priority       string `json:"priority"`
}

// [DÜZELTME 1]: Kafka'dan okunacak ve Outbox'a yazılacak iç payload
type NotificationPayload struct {
	ID       string `json:"id"`
	CorrelationID string `json:"correlation_id"`
	Recipient string `json:"recipient"`
	Channel  string `json:"channel"`
	Content  string `json:"content"`
	Priority string `json:"priority"`
}

type OutboxEvent struct {
	ID        string
	Aggregate string
	Payload   []byte
}

// API'den dışarıya dönülecek bildirim modeli
type NotificationResponse struct {
	ID             string     `json:"id"`
	BatchID        *string    `json:"batch_id,omitempty"`
	ExternalID     *string    `json:"external_id,omitempty"`
	IdempotencyKey string     `json:"idempotency_key"`
	Recipient      string     `json:"recipient"`
	Channel        string     `json:"channel"`
	Content        string     `json:"content"`
	Priority       string     `json:"priority"`
	Status         string     `json:"status"`
	RetryCount     int        `json:"retry_count"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

var smsPhoneRegex = regexp.MustCompile(`^\+?[1-9]\d{7,14}$`)

// Validate, API'den gelen isteğin iş kurallarına uygunluğunu denetler
func (r *NotificationRequest) Validate() error {
	// Veriyi temizle (Boşlukları al, küçük harfe çevir)
	r.Channel = strings.ToLower(strings.TrimSpace(r.Channel))
	r.Priority = strings.ToLower(strings.TrimSpace(r.Priority))
	r.Recipient = strings.TrimSpace(r.Recipient)
	r.Content = strings.TrimSpace(r.Content)

	// 1. Zorunlu Alan Kontrolleri
	if r.Recipient == "" {
		return errors.New("recipient alani zorunludur")
	}
	if r.Content == "" {
		return errors.New("content (mesaj icerigi) zorunludur")
	}
	// ---> [YENİ]: TEKİL MESAJ BOYUT LİMİTİ <---
	// Mesaj içeriğinin 2 KB'ı geçmesine izin verme (Zehirli Hap Koruması)
	if len(r.Content) > 2048 {
		return errors.New("content boyutu cok buyuk (maksimum 2048 byte)")
	}
	// ------------------------------------------
	
	r.IdempotencyKey = strings.TrimSpace(r.IdempotencyKey)
	if r.IdempotencyKey == "" {
		return errors.New("idempotency_key zorunludur")
	}
	if len(r.IdempotencyKey) > 255 {
		return errors.New("idempotency_key en fazla 255 karakter olabilir")
	}
	
	// 2. Channel ve Karakter Sınırı Kontrolleri
	switch r.Channel {
	case "sms":
		if len(r.Content) > 160 {
			return errors.New("sms icerigi 160 karakteri gecemez")
		}
		if !smsPhoneRegex.MatchString(r.Recipient) {
			return errors.New("sms kanalı için E.164 formatında numara gerekli")
		}
	case "email":
		if len(r.Content) > 2048 {
			return errors.New("email icerigi 2048 karakteri gecemez")
		}
		if !strings.Contains(r.Recipient, "@") {
			return errors.New("email kanalı için geçerli bir e-posta adresi gerekli")
		}
	case "push":
		if len(r.Content) > 512 {
			return errors.New("push bildirim icerigi 512 karakteri gecemez")
		}
	default:
		return fmt.Errorf("gecersiz channel: '%s' (gecerli degerler: sms, email, push)", r.Channel)
	}

	// 3. Priority Kontrolü (Boşsa varsayılan ata)
	switch r.Priority {
	case "low", "normal", "high":
		// geçerli
	case "":
		r.Priority = "normal" // Varsayılan değer
	default:
		return fmt.Errorf("gecersiz priority: '%s' (gecerli degerler: low, normal, high)", r.Priority)
	}

	return nil
}
