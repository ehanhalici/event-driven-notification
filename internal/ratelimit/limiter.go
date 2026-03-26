package ratelimit

import (
	"context"
	"time"
	"strconv"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)


type Limiter struct {
	rdb *redis.Client 
}

func NewLimiter(rdb *redis.Client) *Limiter {
	return &Limiter{rdb: rdb}
}


// -------------------------------------------------------------------------
// 1. FIXED WINDOW ALGORİTMASI (Optimize Etdilen Kusursuz Lua Script'i)
// Çok hızlıdır, hafıza dostudur (sadece 1 integer tutar). 
// Dezavantajı: Saniye geçişlerinde 2x burst'e izin verebilir.
// -------------------------------------------------------------------------
var fixedWindowScript = redis.NewScript(`
	local current = redis.call("INCR", KEYS[1])
	if current == 1 then
		redis.call("EXPIRE", KEYS[1], tonumber(ARGV[1]))
	end
	if current > tonumber(ARGV[2]) then
		return 0 -- Reddedildi (Limit Aşıldı)
	end
	return 1 -- Kabul Edildi
`)

// AllowFixed, basit ve ultra-hızlı rate limit kontrolü yapar.
func (l *Limiter) AllowFixed(ctx context.Context, channel string, limit int, ttlSeconds int) (bool, error) {
	// Key saniyeye bağlı olmalı (örn: rate_limit:sms:1710000000)
	nowSec := time.Now().Unix()
	key := "rate_limit:" + channel + ":" + intToStr(nowSec)

	// Lua Script: KEYS[1] = key, ARGV[1] = TTL, ARGV[2] = Limit
	res, err := fixedWindowScript.Run(ctx, l.rdb, []string{key}, ttlSeconds, limit).Result()
	if err != nil {
		return false, err
	}
	return res.(int64) == 1, nil
}


// -------------------------------------------------------------------------
// 2. SLIDING WINDOW LOG ALGORİTMASI (Alternatif / Kesin Çözüm)
// Matematiksel olarak %100 kesindir, 2x burst zafiyeti (Boundary Problem) yoktur.
// Dezavantajı: ZSET kullandığı için Redis'te daha çok hafıza (memory) tüketir.
// -------------------------------------------------------------------------
var slidingWindowScript = redis.NewScript(`
	local key = KEYS[1]
	local window = tonumber(ARGV[1])
	local limit = tonumber(ARGV[2])
	local req_id = ARGV[3]

	-- 1. Redis'in kendi iç saatini al (Zaman kayması problemini önler)
	-- redis.call('TIME') -> { "saniye", "mikrosaniye" } döner
	local redis_time = redis.call('TIME')
	
	-- Saniye ve mikrosaniyeyi birleştirerek 'milisaniye' cinsinden şimdiki zamanı bul
	local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

	-- 2. Pencere dışında kalan eski kayıtları sil
	local clearBefore = now - window
	redis.call('ZREMRANGEBYSCORE', key, 0, clearBefore)
	
	-- 3. Penceredeki mevcut istek sayısını al
	local current_count = redis.call('ZCARD', key)
	
	-- 4. Eğer limit aşılmadıysa yeni isteği kaydet
	if current_count < limit then
		redis.call('ZADD', key, now, now .. '-' .. req_id)
		-- ZSET'in sonsuza kadar bellekte kalmaması için TTL ekle
		redis.call('EXPIRE', key, math.ceil(window/1000) + 1)
		return 1 -- Kabul Edildi
	end
	
	return 0 -- Reddedildi
`)

// AllowSliding, kesin sınır (strict limit) gereken yerlerde kullanılır.
func (l *Limiter) AllowSliding(ctx context.Context, channel string, limit int) (bool, error) {
	key := "rate_limit:sliding:" + channel
	windowMs := 1000 // 1 saniye

	reqID := uuid.New().String()

	// DİKKAT: Artık Go'nun saati olan 'nowMs' parametresini GÖNDERMİYORUZ!
	// Zaman Redis tarafında Lua script içinde hesaplanıyor.
	res, err := slidingWindowScript.Run(ctx, l.rdb, []string{key}, windowMs, limit, reqID).Result()
	if err != nil {
		return false, err
	}
	return res.(int64) == 1, nil
}

// Yardımcı fonksiyon
func intToStr(i int64) string {
	return strconv.FormatInt(i, 10)
}

// -------------------------------------------------------------------------
// TOKEN BUCKET ALGORİTMASI (Ağırlık/Weight Bazlı Kusursuz Limitleyici)
// İstek (Request) değil, Jeton (Token) sayar. Toplu (Batch) işlemler için zorunludur.
// -------------------------------------------------------------------------
var tokenBucketScript = redis.NewScript(`
	local key = KEYS[1]
	local requested = tonumber(ARGV[1])
	local capacity = tonumber(ARGV[2])
	local refill_rate = tonumber(ARGV[3]) -- Saniyede dolacak jeton

	-- Redis iç saatini al (Clock Drift önlemi)
	local redis_time = redis.call('TIME')
	local now = tonumber(redis_time[1]) + (tonumber(redis_time[2]) / 1000000)

	local bucket = redis.call('HMGET', key, 'tokens', 'last_refill')
	local tokens = tonumber(bucket[1])
	local last_refill = tonumber(bucket[2])

	-- İlk kez geliyorsa kovayı tam kapasite doldur
	if not tokens then
		tokens = capacity
		last_refill = now
	else
		-- Geçen süreye göre kovaya yeni jetonları ekle (Kapasiteyi aşamaz)
		local elapsed = math.max(0, now - last_refill)
		tokens = math.min(capacity, tokens + (elapsed * refill_rate))
		last_refill = now
	end

	-- Yeterli jeton var mı?
	if tokens >= requested then
		tokens = tokens - requested
		redis.call('HMSET', key, 'tokens', tokens, 'last_refill', last_refill)
		redis.call('EXPIRE', key, math.ceil(capacity / refill_rate) + 1)
		return 1 -- Kabul edildi
	else
		-- Yeterli jeton yoksa sadece zamanı güncelle, jeton düşme
		redis.call('HMSET', key, 'tokens', tokens, 'last_refill', last_refill)
		redis.call('EXPIRE', key, math.ceil(capacity / refill_rate) + 1)
		return 0 -- Reddedildi (Too Many Requests)
	end
`)

// AllowTokens, belirtilen ağırlıkta (weight) jeton harcamayı dener.
func (l *Limiter) AllowTokens(ctx context.Context, key string, tokens, capacity, refillRate int) (bool, error) {
	res, err := tokenBucketScript.Run(ctx, l.rdb, []string{key}, tokens, capacity, refillRate).Result()
	if err != nil {
		return false, err
	}
	return res.(int64) == 1, nil
}
