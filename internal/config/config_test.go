package config

import (
	"os"
	"testing"
)

// --- getEnv Testleri ---

func TestGetEnv_WithValue(t *testing.T) {
	os.Setenv("TEST_CONFIG_VAR", "custom_value")
	defer os.Unsetenv("TEST_CONFIG_VAR")

	val := getEnv("TEST_CONFIG_VAR", "default")
	if val != "custom_value" {
		t.Errorf("Beklenen 'custom_value', alinan '%s'", val)
	}
}

func TestGetEnv_WithDefault(t *testing.T) {
	os.Unsetenv("NONEXISTENT_CONFIG_VAR")

	val := getEnv("NONEXISTENT_CONFIG_VAR", "fallback_value")
	if val != "fallback_value" {
		t.Errorf("Beklenen 'fallback_value', alinan '%s'", val)
	}
}

func TestGetEnv_EmptyValue(t *testing.T) {
	os.Setenv("EMPTY_VAR", "")
	defer os.Unsetenv("EMPTY_VAR")

	val := getEnv("EMPTY_VAR", "default")
	// os.LookupEnv, bos string icin de exists=true doner
	if val != "" {
		t.Errorf("Beklenen bos string, alinan '%s'", val)
	}
}

// --- Load Testleri ---

func TestLoad_DefaultValues(t *testing.T) {
	// Tum ilgili ortam degiskenlerini temizle
	keys := []string{"DATABASE_URL", "REDIS_URL", "KAFKA_BROKER", "WEBHOOK_URL", "API_PORT"}
	for _, k := range keys {
		os.Unsetenv(k)
	}

	cfg := Load()

	if cfg.DatabaseURL != "postgres://localhost:5432/notifications_db" {
		t.Errorf("Beklenen varsayilan DATABASE_URL, alinan '%s'", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "localhost:6379" {
		t.Errorf("Beklenen varsayilan REDIS_URL, alinan '%s'", cfg.RedisURL)
	}
	if cfg.KafkaBroker != "localhost:9092" {
		t.Errorf("Beklenen varsayilan KAFKA_BROKER, alinan '%s'", cfg.KafkaBroker)
	}
	if cfg.WebhookURL != "https://webhook.site/default" {
		t.Errorf("Beklenen varsayilan WEBHOOK_URL, alinan '%s'", cfg.WebhookURL)
	}
	if cfg.APIPort != ":8080" {
		t.Errorf("Beklenen varsayilan API_PORT ':8080', alinan '%s'", cfg.APIPort)
	}
}

func TestLoad_CustomValues(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://custom:5432/mydb")
	os.Setenv("REDIS_URL", "redis.custom:6379")
	os.Setenv("KAFKA_BROKER", "kafka.custom:9092")
	os.Setenv("WEBHOOK_URL", "https://custom.webhook.site/abc")
	os.Setenv("API_PORT", ":9090")
	defer func() {
		for _, k := range []string{"DATABASE_URL", "REDIS_URL", "KAFKA_BROKER", "WEBHOOK_URL", "API_PORT"} {
			os.Unsetenv(k)
		}
	}()

	cfg := Load()

	if cfg.DatabaseURL != "postgres://custom:5432/mydb" {
		t.Errorf("Beklenen ozel DATABASE_URL, alinan '%s'", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis.custom:6379" {
		t.Errorf("Beklenen ozel REDIS_URL, alinan '%s'", cfg.RedisURL)
	}
	if cfg.KafkaBroker != "kafka.custom:9092" {
		t.Errorf("Beklenen ozel KAFKA_BROKER, alinan '%s'", cfg.KafkaBroker)
	}
	if cfg.WebhookURL != "https://custom.webhook.site/abc" {
		t.Errorf("Beklenen ozel WEBHOOK_URL, alinan '%s'", cfg.WebhookURL)
	}
	if cfg.APIPort != ":9090" {
		t.Errorf("Beklenen ozel API_PORT, alinan '%s'", cfg.APIPort)
	}
}

// --- Validate Testleri ---

func TestValidate_DefaultWebhookShouldPass(t *testing.T) {
	// Validate() artik WEBHOOK_URL kontrolu yapmiyor (sadece uyari loglar).
	// WEBHOOK_URL kontrolu ValidateWorker()'da yapilir.
	cfg := &Config{
		DatabaseURL: "postgres://custom:5432/db",
		RedisURL:    "custom:6379",
		KafkaBroker: "custom:9092",
		WebhookURL:  "https://webhook.site/default",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() API/Relay icin hata vermemeli, alinan: %v", err)
	}
}

func TestValidate_CustomWebhookShouldPass(t *testing.T) {
	cfg := &Config{
		DatabaseURL: "postgres://custom:5432/db",
		RedisURL:    "custom:6379",
		KafkaBroker: "custom:9092",
		WebhookURL:  "https://my-actual-webhook.com/endpoint",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Gecerli webhook URL ile Validate basarisiz olmamali, hata: %v", err)
	}
}

// --- ValidateWorker Testleri ---

func TestValidateWorker_DefaultWebhookShouldFail(t *testing.T) {
	cfg := &Config{
		DatabaseURL: "postgres://custom:5432/db",
		RedisURL:    "custom:6379",
		KafkaBroker: "custom:9092",
		WebhookURL:  "https://webhook.site/default",
	}
	if err := cfg.ValidateWorker(); err == nil {
		t.Error("Varsayilan webhook URL ile ValidateWorker basarili olmamali")
	}
}

func TestValidateWorker_EmptyWebhookShouldFail(t *testing.T) {
	cfg := &Config{
		DatabaseURL: "postgres://custom:5432/db",
		RedisURL:    "custom:6379",
		KafkaBroker: "custom:9092",
		WebhookURL:  "",
	}
	if err := cfg.ValidateWorker(); err == nil {
		t.Error("Bos webhook URL ile ValidateWorker basarili olmamali")
	}
}

func TestValidateWorker_CustomWebhookShouldPass(t *testing.T) {
	cfg := &Config{
		DatabaseURL: "postgres://custom:5432/db",
		RedisURL:    "custom:6379",
		KafkaBroker: "custom:9092",
		WebhookURL:  "https://real-production-webhook.com/api",
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Errorf("Gecerli webhook URL ile ValidateWorker basarisiz olmamali, hata: %v", err)
	}
}
