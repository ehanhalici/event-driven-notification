package ratelimit

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

// =====================================================================
// UNIT TESTLER (Redis gerektirmeyen)
// =====================================================================

func TestNewLimiter_NotNil(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()

	limiter := NewLimiter(rdb)
	if limiter == nil {
		t.Error("NewLimiter nil donmemeli")
	}
	if limiter.rdb == nil {
		t.Error("Limiter.rdb nil olmamali")
	}
}

func TestIntToStr_Positive(t *testing.T) {
	result := intToStr(12345)
	if result != "12345" {
		t.Errorf("Beklenen '12345', alinan '%s'", result)
	}
}

func TestIntToStr_Zero(t *testing.T) {
	result := intToStr(0)
	if result != "0" {
		t.Errorf("Beklenen '0', alinan '%s'", result)
	}
}

func TestIntToStr_Negative(t *testing.T) {
	result := intToStr(-99)
	if result != "-99" {
		t.Errorf("Beklenen '-99', alinan '%s'", result)
	}
}

func TestIntToStr_LargeNumber(t *testing.T) {
	result := intToStr(1710000000)
	if result != "1710000000" {
		t.Errorf("Beklenen '1710000000', alinan '%s'", result)
	}
}

// =====================================================================
// INTEGRATION TESTLER (Redis gerektiren)
// =====================================================================

func setupRedis(t *testing.T) (*redis.Client, context.Context) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis ayakta degil, test atlandi.")
	}
	return rdb, ctx
}

func TestLimiter_AllowSliding(t *testing.T) {
	rdb, ctx := setupRedis(t)
	defer rdb.Close()

	limiter := NewLimiter(rdb)
	channel := "test_sliding_channel_unit"
	limit := 5

	// Sadece bu testin anahtarini temizle
	rdb.Del(ctx, "rate_limit:sliding:"+channel)

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
	rdb, ctx := setupRedis(t)
	defer rdb.Close()

	limiter := NewLimiter(rdb)
	key := "test_token_bucket_unit"
	rdb.Del(ctx, key)

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

func TestLimiter_AllowFixed(t *testing.T) {
	rdb, ctx := setupRedis(t)
	defer rdb.Close()

	limiter := NewLimiter(rdb)
	channel := "test_fixed_window_unit"
	limit := 3
	ttl := 10 // 10 saniye TTL

	// Fixed window key'leri saniyeye bağlı olduğu için temizlik zor;
	// Düşük limit ile hızlı test yapıyoruz
	for i := 1; i <= limit; i++ {
		allowed, err := limiter.AllowFixed(ctx, channel, limit, ttl)
		if err != nil {
			t.Fatalf("AllowFixed hata verdi: %v", err)
		}
		if !allowed {
			t.Errorf("%d. istek reddedildi, limit (%d) asılmamisti", i, limit)
		}
	}

	// Limiti aşan istek
	allowed, err := limiter.AllowFixed(ctx, channel, limit, ttl)
	if err != nil {
		t.Fatalf("AllowFixed hata verdi: %v", err)
	}
	if allowed {
		t.Error("Fixed window limiti asildi ama istek kabul edildi!")
	}
}

func TestLimiter_AllowTokens_ExactCapacity(t *testing.T) {
	rdb, ctx := setupRedis(t)
	defer rdb.Close()

	limiter := NewLimiter(rdb)
	key := "test_token_exact_cap"
	rdb.Del(ctx, key)

	// Tam kapasiteye eşit talep (10 jeton, 10 kapasite) → geçmeli
	allowed, err := limiter.AllowTokens(ctx, key, 10, 10, 1)
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if !allowed {
		t.Error("Tam kapasiteye eşit istek reddedilmemeli")
	}

	// Artık 0 jeton kaldı, 1 jeton bile istenmeli → reddedilmeli
	allowed, _ = limiter.AllowTokens(ctx, key, 1, 10, 1)
	if allowed {
		t.Error("Boş kovadan jeton çekilebildi!")
	}
}

func TestLimiter_AllowTokens_ZeroTokenRequest(t *testing.T) {
	rdb, ctx := setupRedis(t)
	defer rdb.Close()

	limiter := NewLimiter(rdb)
	key := "test_token_zero_req"
	rdb.Del(ctx, key)

	// 0 jeton istenmesi → izin verilmeli (0 >= 0)
	allowed, err := limiter.AllowTokens(ctx, key, 0, 10, 1)
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if !allowed {
		t.Error("0 jetonluk istek reddedilmemeli")
	}
}

func TestLimiter_AllowSliding_SingleRequest(t *testing.T) {
	rdb, ctx := setupRedis(t)
	defer rdb.Close()

	limiter := NewLimiter(rdb)
	channel := "test_sliding_single"
	rdb.Del(ctx, "rate_limit:sliding:"+channel)

	// Limit 1 ile sadece 1 istek gecmeli
	allowed, err := limiter.AllowSliding(ctx, channel, 1)
	if err != nil {
		t.Fatalf("Hata: %v", err)
	}
	if !allowed {
		t.Error("Ilk istek izin verilmeliydi")
	}

	// 2. istek reddedilmeli
	allowed, _ = limiter.AllowSliding(ctx, channel, 1)
	if allowed {
		t.Error("Limit 1 iken 2. istek kabul edildi!")
	}
}
