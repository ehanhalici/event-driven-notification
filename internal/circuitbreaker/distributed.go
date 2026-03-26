package circuitbreaker

import (
	"context"
	"errors"
	"time"
	"github.com/redis/go-redis/v9"
)

var ErrCircuitOpen = errors.New("circuit breaker is open (redis-backed)")

// checkScript, isteğin geçip geçmeyeceğine ve Half-Open probe (test) olup olmadığına karar verir.
var checkScript = redis.NewScript(`
	local stateKey = KEYS[1]
	local halfOpenKey = KEYS[2]
	local probeKey = KEYS[3]

	-- 1. Devre AÇIK ise (Şalter inik) direkt reddet
	if redis.call("EXISTS", stateKey) == 1 then
		return 1 -- 1 = OPEN
	end

	-- 2. Devre süresi dolmuş ama 'halfOpenKey' duruyorsa, sistem HALF-OPEN (Yarı Açık) durumundadır.
	if redis.call("EXISTS", halfOpenKey) == 1 then
		-- Sadece 1 adet "Test İsteği" (Probe) geçmesine izin ver. (NX = Sadece yoksa yaz)
		-- Probe kilit süresi 10 saniye (Test isteği takılırsa sonsuza kadar kilitli kalmasın diye)
		local setProbe = redis.call("SET", probeKey, "1", "NX", "EX", 10)
		if setProbe then
			return 2 -- 2 = HALF_OPEN_ALLOW (Seçilmiş test isteği sensin, geçebilirsin)
		else
			return 1 -- 1 = OPEN (Başka bir worker test isteğini yolladı bile, sen bekle)
		end
	end

	-- 3. Her şey normal
	return 0 -- 0 = CLOSED
`)

// failScript, hata kaydetme ve şalteri indirme işlemini ATOMİK (tek seferde) yapar.
// Race condition ve Thundering Herd zafiyetlerini engeller.
var failScript = redis.NewScript(`
	local stateKey = KEYS[1]
	local halfOpenKey = KEYS[2]
	local probeKey = KEYS[3]
	local failKey = KEYS[4]
	
	local maxFailures = tonumber(ARGV[1])
	local windowSec = tonumber(ARGV[2])
	local openTimeoutSec = tonumber(ARGV[3])
	local isHalfOpenProbe = tonumber(ARGV[4])

	-- Eğer bu başarısız olan istek "Half-Open Test İsteği" ise devreyi ANINDA geri aç!
	if isHalfOpenProbe == 1 then
		redis.call("SET", stateKey, "1", "EX", openTimeoutSec)
        redis.call("SET", halfOpenKey, "1", "EX", openTimeoutSec + 5)
		redis.call("DEL", probeKey)
		return 2 -- OPENED
	end

	-- Normal (Closed) durumdaki hatalar:
	if redis.call("EXISTS", stateKey) == 1 then
		return 1 -- Zaten açık
	end

	local fails = redis.call("INCR", failKey)
	if fails == 1 then
		redis.call("EXPIRE", failKey, windowSec)
	end

	-- Hata eşiği aşıldıysa devreyi AÇ
	if fails >= maxFailures then
		redis.call("SET", stateKey, "1", "EX", openTimeoutSec)
		redis.call("SET", halfOpenKey, "1", "EX", openTimeoutSec + 5) -- Devre süresi bitince Half-Open'a düşmesi için iz bırak
		redis.call("DEL", failKey)
		return 2 -- OPENED
	end

	return 0
`)

type DistributedCB struct {
	rdb         *redis.Client
	name        string
	maxFailures int64
	window      time.Duration
	openTimeout time.Duration
}

// NewDistributedCB, merkezi bir devre kesici oluşturur.
func NewDistributedCB(rdb *redis.Client, name string, maxFailures int64, window, openTimeout time.Duration) *DistributedCB {
	return &DistributedCB{
		rdb:         rdb,
		name:        name,
		maxFailures: maxFailures,
		window:      window,
		openTimeout: openTimeout,
	}
}

func (c *DistributedCB) Execute(ctx context.Context, req func() (interface{}, error)) (interface{}, error) {
	stateKey := "cb:state:" + c.name
	halfOpenKey := "cb:halfopen:" + c.name
	probeKey := "cb:probe:" + c.name
	failKey := "cb:fails:" + c.name

	// 1. Durum Kontrolü (Check)
	status, err := checkScript.Run(ctx, c.rdb, []string{stateKey, halfOpenKey, probeKey}).Result()
	if err != nil {
		// Redis çökerse sistemi kilitleme (Fail-Open)
	} else if status.(int64) == 1 {
		return nil, ErrCircuitOpen
	}

	// Bu isteğin "Half-Open Test İsteği" olup olmadığını kaydediyoruz
	isProbe := 0
	if status != nil && status.(int64) == 2 {
		isProbe = 1
	}

	// 2. İsteği Çalıştır
	res, reqErr := req()

	// 3. Başarısızlık Durumu (Hata Yönetimi)
	if reqErr != nil {
		windowSec := int(c.window.Seconds())
		if windowSec == 0 { windowSec = 1 }
		openTimeoutSec := int(c.openTimeout.Seconds())
		if openTimeoutSec == 0 { openTimeoutSec = 1 }

		_, _ = failScript.Run(ctx, c.rdb, []string{stateKey, halfOpenKey, probeKey, failKey}, c.maxFailures, windowSec, openTimeoutSec, isProbe).Result()
		
		return res, reqErr
	}

	// 4. BAŞARI DURUMU (Self-Healing)
	// İstek başarılı oldu! Eğer bu bir Test (Probe) isteğiyse veya normal bir istekse,
	// sistemi tamamen sağlıklı (Closed) duruma getiriyoruz. Tüm kalıntıları siliyoruz.
	c.rdb.Del(ctx, failKey, stateKey, halfOpenKey, probeKey)

	return res, nil
}
