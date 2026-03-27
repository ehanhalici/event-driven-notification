package models

import (
	"testing"
)

func TestValidate_RequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		req     NotificationRequest
		wantErr bool
		errMsg  string
	}{
		{
			name:    "bos recipient reddedilmeli",
			req:     NotificationRequest{Content: "test", Channel: "sms", IdempotencyKey: "k1"},
			wantErr: true,
			errMsg:  "recipient alani zorunludur",
		},
		{
			name:    "bos content reddedilmeli",
			req:     NotificationRequest{Recipient: "+905551234567", Channel: "sms", IdempotencyKey: "k1"},
			wantErr: true,
			errMsg:  "content (mesaj icerigi) zorunludur",
		},
		{
			name:    "bos idempotency_key reddedilmeli",
			req:     NotificationRequest{Recipient: "+905551234567", Channel: "sms", Content: "test"},
			wantErr: true,
			errMsg:  "idempotency_key zorunludur",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if !tc.wantErr {
				if err != nil {
					t.Errorf("Hata beklenmiyordu, alinan: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("Hata bekleniyordu ('%s'), ama nil alindi", tc.errMsg)
			}
		})
	}
}

func TestValidate_ChannelValidation(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		wantErr bool
	}{
		{"gecerli sms", "sms", false},
		{"gecerli email", "email", false},
		{"gecerli push", "push", false},
		{"gecersiz kanal", "telegram", true},
		{"bos kanal", "", true},
		{"buyuk harfli sms", "SMS", false}, // Normalize edilmeli
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := NotificationRequest{
				Recipient:      "+905551234567",
				Channel:        tc.channel,
				Content:        "test",
				Priority:       "normal",
				IdempotencyKey: "key-1",
			}
			// sms kanal dışı test için email adresi gerekiyor
			if tc.channel == "email" {
				req.Recipient = "test@example.com"
			}
			err := req.Validate()
			if tc.wantErr && err == nil {
				t.Error("Hata bekleniyordu ama nil alindi")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Hata beklenmiyordu, alinan: %v", err)
			}
		})
	}
}

func TestValidate_PriorityDefaults(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "test",
		Priority:       "", // Boş bırakılmış
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Hata beklenmiyordu: %v", err)
	}
	if req.Priority != "normal" {
		t.Errorf("Bos priority 'normal' olmali, alinan: %s", req.Priority)
	}
}

func TestValidate_PriorityInvalid(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "test",
		Priority:       "urgent", // Geçersiz
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err == nil {
		t.Error("Gecersiz priority kabul edilmemeli")
	}
}

func TestValidate_SMSCharacterLimit(t *testing.T) {
	longContent := make([]byte, 161) // 161 karakter
	for i := range longContent {
		longContent[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        string(longContent),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err == nil {
		t.Error("161 karakterlik SMS icerigi reddedilmeliydi")
	}
}

func TestValidate_SMS160CharsOK(t *testing.T) {
	content := make([]byte, 160) // Tam 160 karakter
	for i := range content {
		content[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        string(content),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("160 karakterlik SMS icerigi kabul edilmeliydi, hata: %v", err)
	}
}

func TestValidate_EmailCharacterLimit(t *testing.T) {
	longContent := make([]byte, 2049)
	for i := range longContent {
		longContent[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "test@example.com",
		Channel:        "email",
		Content:        string(longContent),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err == nil {
		t.Error("2049 byte'lik email icerigi reddedilmeliydi")
	}
}

func TestValidate_PushCharacterLimit(t *testing.T) {
	longContent := make([]byte, 513)
	for i := range longContent {
		longContent[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "device-token-123",
		Channel:        "push",
		Content:        string(longContent),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err == nil {
		t.Error("513 byte'lik push icerigi reddedilmeliydi")
	}
}

func TestValidate_SMSRecipientFormat(t *testing.T) {
	tests := []struct {
		name      string
		recipient string
		wantErr   bool
	}{
		{"gecerli E.164", "+905551234567", false},
		{"gecerli artizsiz", "905551234567", false},
		{"cok kisa numara", "+123", true},
		{"email degil", "test@example.com", true},
		{"bos", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := NotificationRequest{
				Recipient:      tc.recipient,
				Channel:        "sms",
				Content:        "test",
				Priority:       "normal",
				IdempotencyKey: "key-1",
			}
			err := req.Validate()
			if tc.wantErr && err == nil {
				t.Error("Hata bekleniyordu ama nil alindi")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Hata beklenmiyordu, alinan: %v", err)
			}
		})
	}
}

func TestValidate_EmailRecipientFormat(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "not-an-email",
		Channel:        "email",
		Content:        "test",
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err == nil {
		t.Error("@ isareti olmayan email adresi reddedilmeliydi")
	}
}

func TestValidate_IdempotencyKeyMaxLength(t *testing.T) {
	longKey := make([]byte, 256)
	for i := range longKey {
		longKey[i] = 'k'
	}

	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "test",
		Priority:       "normal",
		IdempotencyKey: string(longKey),
	}
	if err := req.Validate(); err == nil {
		t.Error("256 karakterlik idempotency_key reddedilmeliydi (max 255)")
	}
}

func TestValidate_ContentMaxBytesLimit(t *testing.T) {
	longContent := make([]byte, 2049)
	for i := range longContent {
		longContent[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "device-token-123",
		Channel:        "push",
		Content:        string(longContent),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err == nil {
		t.Error("2049 byte'lik content reddedilmeliydi (max 2048)")
	}
}

func TestValidate_Normalization(t *testing.T) {
	req := NotificationRequest{
		Recipient:      " +905551234567 ",
		Channel:        "  SMS  ",
		Content:        " test content ",
		Priority:       "  HIGH  ",
		IdempotencyKey: " key-1 ",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Hata beklenmiyordu: %v", err)
	}
	if req.Channel != "sms" {
		t.Errorf("Channel 'sms' olmali, alinan: %s", req.Channel)
	}
	if req.Priority != "high" {
		t.Errorf("Priority 'high' olmali, alinan: %s", req.Priority)
	}
	if req.Recipient != "+905551234567" {
		t.Errorf("Recipient trimmed olmali, alinan: '%s'", req.Recipient)
	}
}
