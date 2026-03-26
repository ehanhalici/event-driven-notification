package ratelimit

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestLimiter_AllowSliding(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()
	ctx := context.Background()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis ayakta degil, Sliding Window testi atlandi.")
	}

	limiter := NewLimiter(rdb)
	channel := "test_sliding_channel"
	limit := 5

	_ = rdb.FlushDB(ctx).Err() // Temizlik

	// Limite kadar başarılı olmalı
	for i := 1; i <= limit; i++ {
		allowed, err := limiter.AllowSliding(ctx, channel, limit)
		if err != nil {
			t.Fatalf("Bilinmeyen hata: %v", err)
		}
		if !allowed {
			t.Errorf("%d. istek reddedildi, oysaki izin verilmeliydi", i)
		}
	}

	// Limiti aşan istek reddedilmeli
	allowed, _ := limiter.AllowSliding(ctx, channel, limit)
	if allowed {
		t.Error("Limit asildi ama istek kabul edildi!")
	}
}

func TestLimiter_AllowTokens(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()
	ctx := context.Background()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis ayakta degil, Token Bucket testi atlandi.")
	}

	limiter := NewLimiter(rdb)
	key := "test_token_bucket"
	_ = rdb.FlushDB(ctx).Err()

	// 10 kapasite, saniyede 1 dolum var. 5 jeton isteyelim.
	allowed, err := limiter.AllowTokens(ctx, key, 5, 10, 1)
	if err != nil {
		t.Fatalf("Bilinmeyen hata: %v", err)
	}
	if !allowed {
		t.Error("5 jetonluk islem reddedildi (izin verilmeliydi)")
	}

	// 6 jeton daha isteyelim, toplam 11 eder. Kapasite (10) asilacagi icin REDDEDILMELI
	allowed, _ = limiter.AllowTokens(ctx, key, 6, 10, 1)
	if allowed {
		t.Error("Kapasiteyi asan islem kabul edildi!")
	}
}
