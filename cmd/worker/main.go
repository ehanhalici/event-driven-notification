package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"errors"
	"net/http"
	
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
	"insider-notification/internal/config"
	"insider-notification/internal/ratelimit"
	"insider-notification/internal/repository"
	"insider-notification/internal/logger"
	"insider-notification/internal/circuitbreaker"
	internalworker "insider-notification/internal/worker"
)

func main() {
	logger.Setup()
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("Konfigurasyon eksik veya hatali (Uygulama baslatilamiyor)", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// İşletim sistemi sinyallerini dinle (SIGINT, SIGTERM)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		slog.Info("Kapanis sinyali alindi, Graceful Shutdown...")
		cancel()
	}()
	
	
	// 1. Veritabanı (Postgres)
	dbPool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("DB baglantisi basarisiz", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// 2. Cache & Rate Limit (Redis)
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisURL,
	})
	defer rdb.Close()

	// Dead Letter Queue Writer'ı başlat
	dlqWriter := &kafka.Writer{
		Addr:     kafka.TCP(cfg.KafkaBroker),
		Topic:    "notifications-dlq",
		Balancer: &kafka.LeastBytes{},
	}
	defer dlqWriter.Close()


	// Dağıtık Devre Kesici (Distributed Circuit Breaker) Ayarları
	// Kural: 10 saniyelik pencerede toplam 10 hata alınırsa, devreyi 30 saniye boyunca TÜM SİSTEM İÇİN aç!
	distCB := circuitbreaker.NewDistributedCB(
		rdb, 
		"webhook_main", // Benzersiz şalter ismi
		10,             // maxFailures
		10*time.Second, // window (Hata sayma penceresi)
		30*time.Second, // openTimeout (Şalterin inik kalacağı süre)
	)

	processor := &internalworker.Processor{
		Repo:       &repository.DB{Pool: dbPool},
		Redis:      rdb,
		Limiter:    ratelimit.NewLimiter(rdb),
		DLQWriter:  dlqWriter,
		CB:         distCB, // [GÜNCELLENDİ]
		WebhookURL: cfg.WebhookURL,
	}
	
	slog.Info("Worker basladi. Kafka topic dinleniyor...")


	// Worker havuzlarını beklemesi için WaitGroup
	var wg sync.WaitGroup

	// YÜZDELİK GOROUTINE DAĞILIMLARI (Toplam 100)
	// %60 High Priority
	startConsumerGroup(ctx, &wg, "notifications-high", processor, 60, cfg)
	// %30 Normal Priority
	startConsumerGroup(ctx, &wg, "notifications-normal", processor, 30, cfg)
	// %10 Low Priority
	startConsumerGroup(ctx, &wg, "notifications-low", processor, 10, cfg)

	slog.Info("Worker havuzlari %60 (High), %30 (Med), %10 (Low) oraninda baslatildi.")

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		slog.Info("Worker Metrik sunucusu baslatildi", "port", ":9091") 
		if err := http.ListenAndServe(":9091", mux); err != nil {
			slog.Error("Metrik sunucusu hatasi", "error", err)
		}
	}()

	// Tüm havuzlar kapanana kadar ana thredi beklet
	wg.Wait()
	slog.Info("Sistem guvenle kapatildi.")
}



// Neden 60 Reader yerine 1 Reader + Worker Pool?
// Çünkü Kafka topic'inin partition sayısından fazla açılan Kafka Reader'lar 
// tamamen boşta (IDLE) bekler ve network/memory sızıntısına yol açar.
// Doğru tasarım: Kafka'dan tek bir Reader ile veriyi olabildiğince hızlı çekip (Fetch), 
// Go channel'lar vasıtasıyla N adet yerel Goroutine (Worker) havuzuna dağıtmaktır.
func startConsumerGroup(ctx context.Context, globalWg *sync.WaitGroup, topic string, processor *internalworker.Processor, workerCount int, cfg *config.Config) {
	
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  []string{cfg.KafkaBroker},
		GroupID:  "notification-workers", 
		Topic:    topic,
		MinBytes: 10e3,
		MaxBytes: 10e6,
	})

	// Mesajların aktarılacağı tamponlu (buffered) kanal.
	// Worker sayısının 2 katı buffer koyarak Fetcher'ın tıkanmasını (blocking) önlüyoruz.
	msgChan := make(chan kafka.Message, workerCount*2)

	// Sadece bu topic'e ait worker'ların kapandığını takip etmek için yerel WaitGroup
	var localWg sync.WaitGroup

	// -------------------------------------------------------------
	// 1. WORKER HAVUZUNU (Pool) BAŞLAT
	// -------------------------------------------------------------
	for i := 0; i < workerCount; i++ {
		globalWg.Add(1)
		localWg.Add(1)
		
		go func(workerID int) {
			defer globalWg.Done()
			defer localWg.Done()

			slog.Info("Worker hazir", "topic", topic, "worker_id", workerID)

			// Kanal açık olduğu sürece (Fetch devam ettikçe) mesajları sırayla al ve işle
			for msg := range msgChan {
				processor.Process(ctx, msg, reader)
			}
			
			slog.Info("Worker kapaniyor...", "topic", topic, "worker_id", workerID)
		}(i)
	}

	// -------------------------------------------------------------
	// 2. KAFKA FETCHER (Okuyucu) GOROUTINE BAŞLAT
	// -------------------------------------------------------------
	globalWg.Add(1)
	go func() {
		defer globalWg.Done()

		slog.Info("Kafka Fetcher baslatildi", "topic", topic)

		for {
			// Sadece Fetch işlemi yap, ağır olan "Process" işlemini Worker'lara bırak
			msg, err := reader.FetchMessage(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					slog.Info("Kafka Fetcher durduruldu (Context Canceled)", "topic", topic)
				} else {
					slog.Error("Kafka'dan mesaj okunamadi", "topic", topic, "error", err)
				}
				break // Hata veya iptal durumunda Fetch döngüsünden çık
			}
			
			// Okunan mesajı havuza (Worker'lara) gönder
			msgChan <- msg
		}

		// --- GRACEFUL SHUTDOWN (ZARİF KAPANIŞ) KOREOGRAFİSİ ---
		
		// 1. Önce kanalı kapatıyoruz ki Worker'lar yeni mesaj gelmeyeceğini anlasın 
		// ve ellerindeki mevcut mesajları bitirince for-range döngüsünden çıksınlar.
		close(msgChan)

		// 2. Worker'ların ellerindeki son mesajları işlemelerini ve Kafka'ya 
		// COMMIT etmelerini bekliyoruz. (Eğer beklemeksizin reader.Close yapsaydık,
		// worker'ların içindeki CommitMessages fonksiyonu hata fırlatırdı!)
		localWg.Wait()

		// 3. Tüm worker'lar işini bitirdikten sonra Reader'ı güvenle kapatıyoruz.
		if err := reader.Close(); err != nil {
			slog.Error("Kafka reader kapatilirken hata", "topic", topic, "error", err)
		} else {
			slog.Info("Kafka Reader guvenle kapatildi", "topic", topic)
		}
	}()
}
