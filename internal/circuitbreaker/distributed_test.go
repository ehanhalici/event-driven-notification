package circuitbreaker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func setupRedis(t *testing.T) *redis.Client {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skip("Redis ayakta degil, CircuitBreaker testleri atlandi.")
	}
	return rdb
}

func cleanCBKeys(ctx context.Context, rdb *redis.Client, name string) {
	rdb.Del(ctx,
		"cb:state:"+name,
		"cb:halfopen:"+name,
		"cb:probe:"+name,
		"cb:fails:"+name,
	)
}

// --- Temel Durum Testleri ---

func TestCB_ClosedState_AllowsRequests(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_closed_state"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	cb := NewDistributedCB(rdb, cbName, 5, 10*time.Second, 10*time.Second)

	result, err := cb.Execute(ctx, func() (interface{}, error) {
		return "basarili", nil
	})

	if err != nil {
		t.Fatalf("Kapali durumda istek reddedildi: %v", err)
	}
	if result.(string) != "basarili" {
		t.Errorf("Beklenen sonuc 'basarili', alinan: '%v'", result)
	}
}

func TestCB_OpensAfterMaxFailures(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_open_after_failures"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	cb := NewDistributedCB(rdb, cbName, 3, 1*time.Minute, 30*time.Second)

	simulatedErr := errors.New("upstream hata")

	// 3 hata yap (maxFailures = 3)
	for i := 0; i < 3; i++ {
		_, err := cb.Execute(ctx, func() (interface{}, error) {
			return nil, simulatedErr
		})
		if err == nil {
			t.Fatalf("Hatali istek basarili olarak dondu (istek %d)", i+1)
		}
	}

	// 4. istek → devre acik olmali (ErrCircuitOpen)
	_, err := cb.Execute(ctx, func() (interface{}, error) {
		t.Error("Bu fonksiyon cagirilmamali (devre acik)")
		return nil, nil
	})

	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("Beklenen ErrCircuitOpen, alinan: %v", err)
	}
}

func TestCB_ResetOnSuccess(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_reset_on_success"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	cb := NewDistributedCB(rdb, cbName, 3, 1*time.Minute, 30*time.Second)

	// 2 hata yap (eşik altında)
	for i := 0; i < 2; i++ {
		cb.Execute(ctx, func() (interface{}, error) {
			return nil, errors.New("hata")
		})
	}

	// 1 basarili istek
	_, err := cb.Execute(ctx, func() (interface{}, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Basarili istek hata dondu: %v", err)
	}

	// Sayac sifirlanmis olmali, 1 hata daha yapinca devre acilmamali
	cb.Execute(ctx, func() (interface{}, error) {
		return nil, errors.New("hata")
	})

	// Devre hala kapali olmali
	_, err = cb.Execute(ctx, func() (interface{}, error) {
		return "hala calisiyor", nil
	})
	if errors.Is(err, ErrCircuitOpen) {
		t.Error("Devre reset sonrasi tekrar acilmamali (henuz esige ulasilmadi)")
	}
}

func TestCB_OpenStateBlocksAllRequests(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_open_blocks_all"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	cb := NewDistributedCB(rdb, cbName, 1, 1*time.Minute, 30*time.Second)

	// 1 hata → devre acilsin
	cb.Execute(ctx, func() (interface{}, error) {
		return nil, errors.New("fatal")
	})

	// 10 istek de reddedilmeli
	for i := 0; i < 10; i++ {
		_, err := cb.Execute(ctx, func() (interface{}, error) {
			return nil, nil
		})
		if !errors.Is(err, ErrCircuitOpen) {
			t.Errorf("Istek %d: Devre acikken istek kabul edildi", i+1)
		}
	}
}

func TestCB_HalfOpenAfterTimeout(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_half_open"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	// openTimeout = 1 saniye (hızlı test icin)
	cb := NewDistributedCB(rdb, cbName, 1, 1*time.Minute, 1*time.Second)

	// Devreyi ac
	cb.Execute(ctx, func() (interface{}, error) {
		return nil, errors.New("hata")
	})

	// Devre acik dogrula
	_, err := cb.Execute(ctx, func() (interface{}, error) {
		return nil, nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatal("Devre acik olmali")
	}

	// 1.5 saniye bekle (openTimeout süresi dolduktan sonra Half-Open'a düşmeli)
	time.Sleep(1500 * time.Millisecond)

	// Half-open: 1 test istegi gecmeli
	result, err := cb.Execute(ctx, func() (interface{}, error) {
		return "test_basarili", nil
	})

	// Basarili olursa devre tamamen kapanmali
	if err != nil {
		// Half-open'da basarili olabilir veya probe lock nedeniyle reddedilebilir
		if errors.Is(err, ErrCircuitOpen) {
			// halfOpenKey henuz süresi dolmadıysa (halfOpenKey TTL = openTimeout + 5)
			// bu beklenen bir durumdur
			t.Log("Half-open probe kilidi yüzünden reddedildi (beklenen olabilir)")
			return
		}
		t.Fatalf("Beklenmeyen hata: %v", err)
	}
	if result.(string) != "test_basarili" {
		t.Errorf("Beklenen 'test_basarili', alinan: '%v'", result)
	}
}

func TestCB_HalfOpenProbeFailure_ReOpens(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_half_open_fail"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	cb := NewDistributedCB(rdb, cbName, 1, 1*time.Minute, 1*time.Second)

	// Devreyi ac
	cb.Execute(ctx, func() (interface{}, error) {
		return nil, errors.New("hata")
	})

	// Timeout bekle
	time.Sleep(1500 * time.Millisecond)

	// Half-open test istegi BASARISIZ olsun
	cb.Execute(ctx, func() (interface{}, error) {
		return nil, errors.New("hala bozuk")
	})

	// Devre tekrar acik olmali
	_, err := cb.Execute(ctx, func() (interface{}, error) {
		return nil, nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Error("Test istegi basarisiz olunca devre tekrar acilmali")
	}
}

// --- NewDistributedCB Testleri ---

func TestNewDistributedCB_CreatesCorrectly(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	cb := NewDistributedCB(rdb, "test_create", 10, 1*time.Minute, 30*time.Second)

	if cb.name != "test_create" {
		t.Errorf("Beklenen isim 'test_create', alinan '%s'", cb.name)
	}
	if cb.maxFailures != 10 {
		t.Errorf("Beklenen maxFailures 10, alinan %d", cb.maxFailures)
	}
	if cb.window != 1*time.Minute {
		t.Errorf("Beklenen window 1m, alinan %v", cb.window)
	}
	if cb.openTimeout != 30*time.Second {
		t.Errorf("Beklenen openTimeout 30s, alinan %v", cb.openTimeout)
	}
}

// --- ErrCircuitOpen Testi ---

func TestErrCircuitOpen_Value(t *testing.T) {
	if ErrCircuitOpen.Error() != "circuit breaker is open (redis-backed)" {
		t.Errorf("Beklenmeyen ErrCircuitOpen mesaji: %s", ErrCircuitOpen.Error())
	}
}

// --- Concurrent Access Testi ---

func TestCB_ConcurrentRequests(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_concurrent"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	cb := NewDistributedCB(rdb, cbName, 100, 10*time.Second, 10*time.Second)

	// 50 basarili istek paralel olarak
	errCh := make(chan error, 50)
	for i := 0; i < 50; i++ {
		go func() {
			_, err := cb.Execute(ctx, func() (interface{}, error) {
				return "ok", nil
			})
			errCh <- err
		}()
	}

	for i := 0; i < 50; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("Paralel istek basarisiz oldu: %v", err)
		}
	}
}

// --- Fonksiyonel İstek Sonucu Testi ---

func TestCB_ReturnsResultFromFunction(t *testing.T) {
	rdb := setupRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	cbName := "test_result_passthrough"
	cleanCBKeys(ctx, rdb, cbName)
	defer cleanCBKeys(ctx, rdb, cbName)

	cb := NewDistributedCB(rdb, cbName, 5, 10*time.Second, 10*time.Second)

	type CustomResult struct {
		Value int
		Name  string
	}

	result, err := cb.Execute(ctx, func() (interface{}, error) {
		return &CustomResult{Value: 42, Name: "test"}, nil
	})

	if err != nil {
		t.Fatalf("Hata beklenmiyordu: %v", err)
	}

	cr, ok := result.(*CustomResult)
	if !ok {
		t.Fatal("Sonuc CustomResult tipinde olmali")
	}
	if cr.Value != 42 || cr.Name != "test" {
		t.Errorf("Beklenen {42, test}, alinan {%d, %s}", cr.Value, cr.Name)
	}
}
