package config

import (
	"errors"
	"os"
	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL  string
	RedisURL     string
	KafkaBrokers []string
	WebhookURL   string
	APIPort      string
}

// Load, önce .env dosyasını arar, yoksa sistem ortam değişkenlerini okur.
func Load() *Config {
	_ = godotenv.Load() // Hata yutulur çünkü production'da .env olmaz, Docker env vars olur

	return &Config{
		DatabaseURL:  getEnv("DATABASE_URL", "postgres://localhost:5432/notifications_db"),
		RedisURL:     getEnv("REDIS_URL", "localhost:6379"),
		KafkaBrokers: []string{getEnv("KAFKA_BROKER", "localhost:9092")},
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
	if c.DatabaseURL == "" { return errors.New("DATABASE_URL zorunlu") }
	if c.RedisURL == "" { return errors.New("REDIS_URL zorunlu") }
	if len(c.KafkaBrokers) == 0 || c.KafkaBrokers[0] == "" { return errors.New("KAFKA_BROKER zorunlu") }
	return nil
}
