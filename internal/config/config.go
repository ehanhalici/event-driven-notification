package config

import (
	"errors"
	"os"
	"log/slog"
	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL  string
	RedisURL     string
	KafkaBroker string
	WebhookURL   string
	APIPort      string
}

// Load, önce .env dosyasını arar, yoksa sistem ortam değişkenlerini okur.
func Load() *Config {
	_ = godotenv.Load() // Hata yutulur çünkü production'da .env olmaz, Docker env vars olur

	return &Config{
		DatabaseURL:  getEnv("DATABASE_URL", "postgres://localhost:5432/notifications_db"),
		RedisURL:     getEnv("REDIS_URL", "localhost:6379"),
		KafkaBroker: getEnv("KAFKA_BROKER", "localhost:9092"),
		WebhookURL:   getEnv("WEBHOOK_URL", "https://webhook.site/default"),
		APIPort:      getEnv("API_PORT", ":8080"),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func (c *Config) Validate() error {
	// Load() fonksiyonunda atanan varsayılan değerler
	defaults := map[string]string{
		"DATABASE_URL": "postgres://localhost:5432/notifications_db",
		"REDIS_URL":    "localhost:6379",
		"KAFKA_BROKER": "localhost:9092",
		"WEBHOOK_URL":  "https://webhook.site/default",
	}

	env := map[string]string{
		"DATABASE_URL": c.DatabaseURL,
		"REDIS_URL":    c.RedisURL,
		"KAFKA_BROKER": c.KafkaBroker,
		"WEBHOOK_URL":  c.WebhookURL,
	}

	// Varsayılan değer kullanımı varsa loglara uyarı bas
	for key, def := range defaults {
		if env[key] == def {
			slog.Warn("DIKKAT: Ortam degiskeni default degerle calisiyor (Production icin riskli olabilir)",
				"key", key,
				"value", def,
			)
		}
	}

	return nil
}

// ValidateWorker, worker servisi için ek validasyonlar yapar.
// WEBHOOK_URL sadece worker servisi tarafından kullanıldığından,
// bu kontrol ortak Validate() yerine burada yapılır.
func (c *Config) ValidateWorker() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.WebhookURL == "https://webhook.site/default" || c.WebhookURL == "" {
		return errors.New("KRITIK HATA: WEBHOOK_URL production ortaminda zorunludur ve default birakilamaz")
	}
	return nil
}
