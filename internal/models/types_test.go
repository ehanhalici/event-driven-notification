package models

import (
	"strings"
	"testing"
)

// =====================================================================
// ZORUNLU ALAN TESTLERİ
// =====================================================================

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

// =====================================================================
// KANAL DOĞRULAMA TESTLERİ
// =====================================================================

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
		{"buyuk harfli sms", "SMS", false},
		{"buyuk harfli EMAIL", "EMAIL", false},
		{"karisik harfli Push", "Push", false},
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
			if strings.EqualFold(tc.channel, "email") {
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

// =====================================================================
// ÖNCELİK (PRIORITY) TESTLERİ
// =====================================================================

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

func TestValidate_AllValidPriorities(t *testing.T) {
	priorities := []string{"low", "normal", "high", "LOW", "NORMAL", "HIGH", "Low", "Normal", "High"}
	for _, p := range priorities {
		t.Run(p, func(t *testing.T) {
			req := NotificationRequest{
				Recipient:      "+905551234567",
				Channel:        "sms",
				Content:        "test",
				Priority:       p,
				IdempotencyKey: "key-1",
			}
			if err := req.Validate(); err != nil {
				t.Errorf("'%s' priority gecerli olmali, hata: %v", p, err)
			}
		})
	}
}

// =====================================================================
// SMS KARAKTER LİMİTİ TESTLERİ
// =====================================================================

func TestValidate_SMSCharacterLimit_161(t *testing.T) {
	longContent := make([]byte, 161)
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
	content := make([]byte, 160)
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

func TestValidate_SMSOneCharOK(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "a",
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("1 karakterlik SMS kabul edilmeliydi, hata: %v", err)
	}
}

// =====================================================================
// EMAIL KARAKTER LİMİTİ TESTLERİ
// =====================================================================

func TestValidate_EmailCharacterLimit_2049(t *testing.T) {
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

func TestValidate_EmailExactly2048OK(t *testing.T) {
	content := make([]byte, 2048)
	for i := range content {
		content[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "test@example.com",
		Channel:        "email",
		Content:        string(content),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Tam 2048 byte email icerigi kabul edilmeliydi, hata: %v", err)
	}
}

// =====================================================================
// PUSH BİLDİRİM KARAKTER LİMİTİ TESTLERİ
// =====================================================================

func TestValidate_PushCharacterLimit_513(t *testing.T) {
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

func TestValidate_PushExactly512OK(t *testing.T) {
	content := make([]byte, 512)
	for i := range content {
		content[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "device-token-123",
		Channel:        "push",
		Content:        string(content),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Tam 512 byte push icerigi kabul edilmeliydi, hata: %v", err)
	}
}

// =====================================================================
// GENEL CONTENT BOYUT LİMİTİ (2048 byte)
// =====================================================================

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

func TestValidate_ContentExactly2048OK_Push(t *testing.T) {
	// Push için genel limit 512. 2048 byte push'ta kanal limiti (512) devreye girer.
	content := make([]byte, 500)
	for i := range content {
		content[i] = 'a'
	}

	req := NotificationRequest{
		Recipient:      "device-token-123",
		Channel:        "push",
		Content:        string(content),
		Priority:       "normal",
		IdempotencyKey: "key-1",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("500 byte push icerigi kabul edilmeliydi, hata: %v", err)
	}
}

// =====================================================================
// SMS ALICI FORMAT TESTLERİ
// =====================================================================

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
		{"sadece arti isareti", "+", true},
		{"gecerli 8 haneli", "+12345678", false},
		{"gecerli 15 haneli (max)", "+123456789012345", false},
		{"cok uzun 16 haneli", "+1234567890123456", true},
		{"harf iceren numara", "+90555abc4567", true},
		{"ozel karakter iceren", "+90555-123-4567", true},
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

// =====================================================================
// EMAIL ALICI FORMAT TESTLERİ
// =====================================================================

func TestValidate_EmailRecipientFormat(t *testing.T) {
	tests := []struct {
		name      string
		recipient string
		wantErr   bool
	}{
		{"gecerli email", "user@example.com", false},
		{"gecerli subdomain email", "user@mail.example.com", false},
		{"gecerli plus email", "user+tag@example.com", false},
		{"@ isareti olmayan", "not-an-email", true},
		{"bos string", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := NotificationRequest{
				Recipient:      tc.recipient,
				Channel:        "email",
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

// =====================================================================
// IDEMPOTENCY KEY TESTLERİ
// =====================================================================

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

func TestValidate_IdempotencyKeyExactly255OK(t *testing.T) {
	key := make([]byte, 255)
	for i := range key {
		key[i] = 'k'
	}

	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "test",
		Priority:       "normal",
		IdempotencyKey: string(key),
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Tam 255 karakterlik idempotency_key kabul edilmeliydi, hata: %v", err)
	}
}

func TestValidate_IdempotencyKeyWithSpaces(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "test",
		Priority:       "normal",
		IdempotencyKey: "  key-with-spaces  ",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Bosluklu idempotency_key trim edilip kabul edilmeliydi, hata: %v", err)
	}
}

func TestValidate_IdempotencyKeyOnlySpaces(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "test",
		Priority:       "normal",
		IdempotencyKey: "    ",
	}
	if err := req.Validate(); err == nil {
		t.Error("Sadece bosluk iceren idempotency_key reddedilmeliydi")
	}
}

// =====================================================================
// NORMALİZASYON TESTLERİ
// =====================================================================

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
	if req.Content != "test content" {
		t.Errorf("Content trimmed olmali, alinan: '%s'", req.Content)
	}
}

// =====================================================================
// TAM GEÇERLİ SENARYO TESTLERİ
// =====================================================================

func TestValidate_FullyValidSMS(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "+905551234567",
		Channel:        "sms",
		Content:        "Merhaba! Bu bir test mesajidir.",
		Priority:       "high",
		IdempotencyKey: "unique-key-001",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Tam gecerli SMS reddedilmemeli, hata: %v", err)
	}
}

func TestValidate_FullyValidEmail(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "user@company.com",
		Channel:        "email",
		Content:        "Bu bir email bildirim icerigidir.",
		Priority:       "normal",
		IdempotencyKey: "email-key-001",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Tam gecerli email reddedilmemeli, hata: %v", err)
	}
}

func TestValidate_FullyValidPush(t *testing.T) {
	req := NotificationRequest{
		Recipient:      "device-token-abc123",
		Channel:        "push",
		Content:        "Yeni bir bildiriminiz var!",
		Priority:       "low",
		IdempotencyKey: "push-key-001",
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Tam gecerli push reddedilmemeli, hata: %v", err)
	}
}

// =====================================================================
// HATA MESAJI DOĞRULAMA TESTLERİ
// =====================================================================

func TestValidate_ErrorMessages(t *testing.T) {
	tests := []struct {
		name     string
		req      NotificationRequest
		contains string
	}{
		{
			name:     "gecersiz channel hata mesaji",
			req:      NotificationRequest{Recipient: "+905551234567", Channel: "fax", Content: "test", IdempotencyKey: "k"},
			contains: "gecersiz channel",
		},
		{
			name:     "gecersiz priority hata mesaji",
			req:      NotificationRequest{Recipient: "+905551234567", Channel: "sms", Content: "test", Priority: "critical", IdempotencyKey: "k"},
			contains: "gecersiz priority",
		},
		{
			name:     "sms karakter limiti hata mesaji",
			req:      NotificationRequest{Recipient: "+905551234567", Channel: "sms", Content: strings.Repeat("a", 161), Priority: "normal", IdempotencyKey: "k"},
			contains: "sms icerigi 160 karakteri gecemez",
		},
		{
			name:     "email format hata mesaji",
			req:      NotificationRequest{Recipient: "not-email", Channel: "email", Content: "test", Priority: "normal", IdempotencyKey: "k"},
			contains: "e-posta adresi gerekli",
		},
		{
			name:     "sms format hata mesaji",
			req:      NotificationRequest{Recipient: "abc", Channel: "sms", Content: "test", Priority: "normal", IdempotencyKey: "k"},
			contains: "E.164",
		},
		{
			name:     "content boyut hata mesaji",
			req:      NotificationRequest{Recipient: "+905551234567", Channel: "sms", Content: strings.Repeat("a", 2049), Priority: "normal", IdempotencyKey: "k"},
			contains: "content boyutu cok buyuk",
		},
		{
			name:     "idempotency key uzunluk hata mesaji",
			req:      NotificationRequest{Recipient: "+905551234567", Channel: "sms", Content: "test", Priority: "normal", IdempotencyKey: strings.Repeat("k", 256)},
			contains: "idempotency_key en fazla 255 karakter",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if err == nil {
				t.Fatalf("Hata bekleniyor ama nil alindi")
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("Hata mesaji '%s' icermeli, alinan: '%s'", tc.contains, err.Error())
			}
		})
	}
}
