package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"
	"os/signal"
	"syscall"
	"net/http"
	
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
	"insider-notification/internal/config"
	"insider-notification/internal/models"
	"insider-notification/internal/logger"
	"insider-notification/internal/metrics"
)

func main() {
	logger.Setup()
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("Konfigurasyon eksik veya hatali (Uygulama baslatilamiyor)", "error", err)
		os.Exit(1)
	}
	// gracefull shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		slog.Info("Relay Worker durduruluyor...")
		cancel()
	}()
	
	// 1. Veritabanı Bağlantısı
	dbPool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("Veritabanina baglanilamadi", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// 2. Kafka Writer (Producer) Ayarları
	// DİKKAT: Topic alanını boş bırakıyoruz ki mesaj bazlı dinamik topic atayabilelim.
	kafkaWriter := &kafka.Writer{
		Addr:     kafka.TCP(cfg.KafkaBrokers[0]),
		// Topic:    "notifications-normal", // Gerçekte priority'ye göre topic seçilebilir
		Balancer: &kafka.LeastBytes{},    // Yükü partition'lara eşit dağıt

		// BatchBytes: Kafka Broker'ın kabul edeceği maksimum mesaj paket boyutu (1 MB)
		// Eğer Relay veritabanından 1 MB'dan fazla veri çekerse, Writer bunları 
		// otomatik olarak 1'er MB'lık alt paketlere (chunk) bölerek Kafka'ya yollar.
		// Böylece MessageTooLarge hatası (Zehirli Hap) fiziksel olarak imkansız hale gelir!
		BatchBytes:   1048576, // 1 MB (1024 * 1024)
		BatchSize:    100,     // DB'den okuma limitimiz olan 100 ile eşitliyoruz
		BatchTimeout: 10 * time.Millisecond,
		RequiredAcks: kafka.RequireAll,
	}
	defer kafkaWriter.Close()

	slog.Info("Relay Worker basladi. Outbox tablosu dinleniyor...")

	// 3. Sonsuz Döngü (Polling)
	ticker := time.NewTicker(1 * time.Second) // Her saniye kontrol et
	defer ticker.Stop()
	stuckJobTicker := time.NewTicker(1 * time.Minute)
	defer stuckJobTicker.Stop()
	
	// ---> METRİK SUNUCUSUNU BURAYA EKLİYORUZ <---
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		slog.Info("Relay Metrik sunucusu baslatildi", "port", ":9094") 
		if err := http.ListenAndServe(":9094", mux); err != nil {
			slog.Error("Metrik sunucusu hatasi", "error", err)
		}
	}()
	// -------------------------------------------
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// DRAIN (Kuyruk Boşaltma) MANTIĞI
			// Veritabanında işlenecek mesaj olduğu sürece, saniye beklemeden art arda çek!
			for {
				processedCount := processOutbox(ctx, dbPool, kafkaWriter)
				
				// Eğer 100'den az kayıt işlendiyse (örn: 0, 45, 99), 
				// kuyruğun o anlık sonuna gelinmiş demektir. Döngüyü kır ve 1 saniye uyu.
				if processedCount < 100 {
					break 
				}
				
				// Eğer tam 100 kayıt işlendiyse, arkada bekleyen DAHA FAZLA mesaj olma 
				// ihtimali çok yüksektir. Döngü kırılmaz, anında bir 100'lük daha çekilir.
				
				// Güvenlik: Sonsuz döngüde kilitli kalırken kapanış sinyali (SIGTERM) gelirse
				// işlemi güvenle iptal edebilmek için context kontrolü:
				if ctx.Err() != nil {
					return
				}
			}
			sweepRetries(ctx, dbPool) // Hayalet Retry'ları hayata döndür

			var queueSize float64
			err := dbPool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox_events WHERE status = 'pending'").Scan(&queueSize)
			if err == nil {
				metrics.OutboxQueueDepth.Set(queueSize) // Prometheus Gauge'ini güncelle
			}
		case <-stuckJobTicker.C:
			// DİKKAT: outbox_events için zombi temizliğine gerek yoktur!
			// FOR UPDATE SKIP LOCKED ve Transaction kullanıldığı için, 
			// çöken Relay'lerin kilitleri anında düşer ve satırlar zaten 'pending' kalır.
			
			// [KRİTİK DÜZELTME] Notifications Zombie Temizliği + Kafka'ya Yeniden Enjeksiyon
			// Worker'lar (Relay değil) mesajı işlerken statüyü kalıcı olarak 'processing' yapar.
			// Eğer worker Kafka'dan mesajı alıp, API'ye istek atarken çökerse, mesaj 'processing' statüsünde kalır.
			// Aşağıdaki Atomik CTE, bu zombileri bulur ve Kafka'ya yeniden atılması için Outbox'a geri besler!
			_, err := dbPool.Exec(ctx, `
				WITH zombie_notifications AS (
					UPDATE notifications 
					SET status = 'pending', updated_at = NOW()
					WHERE status = 'processing' AND updated_at < NOW() - INTERVAL '5 minutes'
					RETURNING id, recipient, channel, content, priority, correlation_id
				)
				INSERT INTO outbox_events (aggregate_id, event_type, payload)
				SELECT id, 'ZombieRecovery', jsonb_build_object(
					'id', id, 
					'recipient', recipient, 
					'channel', channel, 
					'content', content, 
					'priority', priority,
					'correlation_id', correlation_id
				)
				FROM zombie_notifications;
			`)
			if err != nil {
				slog.Error("Zombie notification kurtarma hatasi", "err", err)
			} else {
			    // İsteğe bağlı olarak, kaç zombinin kurtarıldığını loglayabilirsin
			    slog.Info("Stuck job sweeper çalisti")
			}
		}
	}
}

// [DÜZELTME 2]: Atomik CTE ile Retry Süpürücü
func sweepRetries(ctx context.Context, db *pgxpool.Pool) {
	_, err := db.Exec(ctx, `
		WITH locked_retries AS (
			-- [KRİTİK DÜZELTME]: recipient ve correlation_id eklendi!
			SELECT id, recipient, channel, content, priority, correlation_id
			FROM notifications
			WHERE status = 'failed_retrying' AND next_retry_at <= NOW()
            LIMIT 1000
			FOR UPDATE SKIP LOCKED
		),
		updated_notifications AS (
			UPDATE notifications
			SET status = 'pending', updated_at = NOW()
			WHERE id IN (SELECT id FROM locked_retries)
		)
		INSERT INTO outbox_events (aggregate_id, event_type, payload)
		SELECT id, 'NotificationRetry', jsonb_build_object(
			'id', id, 
			'recipient', recipient, 
			'channel', channel, 
			'content', content, 
			'priority', priority,
			'correlation_id', correlation_id -- [İZLENEBİLİRLİK KURTARILDI]
		)
		FROM locked_retries;
	`)
	if err != nil {
		slog.Error("Retry sweeper hatasi", "err", err)
	}
}

func processOutbox(parentCtx context.Context, db *pgxpool.Pool, writer *kafka.Writer) int{
	ctx, cancel := context.WithTimeout(parentCtx, 5*time.Second)
    defer cancel()
	// 1. Transaction Başlat
	tx, err := db.Begin(ctx)
	if err != nil {
		slog.Error("Relay transaction baslatilamadi", "error", err)
		return 0
	}
	// İşlem sonunda hata varsa veya commit edilmediyse rollback yap
	defer tx.Rollback(ctx)
	
	// MATEMATİKSEL KESİNLİK: FOR UPDATE SKIP LOCKED
	// Eğer bu servisten 3 tane çalıştırırsan, aynı outbox kaydını aynı anda okumalarını engeller.
	// Biri satırı kilitler, diğeri kilitli satırı atlayıp (SKIP) bir sonrakini alır.
	// 2. FOR UPDATE SKIP LOCKED ile satırları kilitleyerek oku!
	// Bu sayede aynı anda 5 tane Relay sunucusu çalışsa bile birbirinin mesajını okumazlar.
	rows, err := tx.Query(ctx, `
		SELECT id, payload 
		FROM outbox_events 
		WHERE status = 'pending' 
		ORDER BY created_at ASC 
		LIMIT 100 
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		slog.Error("Outbox sorgusu başarisiz", "error", err)
		return 0
	}
	defer rows.Close()


	var processedIDs []string
	var kafkaMessages []kafka.Message
	for rows.Next() {
		var eventID string
		var payloadBytes []byte
		if err := rows.Scan(&eventID, &payloadBytes); err != nil {
			continue
		}

		var payloadData models.NotificationPayload
		if err := json.Unmarshal(payloadBytes, &payloadData); err != nil {
			slog.Error("Payload parse edilemedi, zehirli mesaj cöpe atiliyor", "id", eventID)
			processedIDs = append(processedIDs, eventID) // BOZUK MESAJI DA SİL Kİ SİSTEM TIKANMASIN!
			continue
		}
		// Önceliğe göre dinamik Topic yönlendirmesi
		topicName := "notifications-normal"
		if payloadData.Priority == "high" {
			topicName = "notifications-high"
		} else if payloadData.Priority == "low" {
			topicName = "notifications-low"
		}

		// Kafka'ya atılacak mesajları biriktir
		kafkaMessages = append(kafkaMessages, kafka.Message{
			Topic: topicName,
			// Key:   []byte(payloadData.ID), // Aynı ID'yi key yapıyoruz ki Kafka aynı partition'a düşürsün
			Key: []byte(payloadData.Recipient), // yukaridaki iptal, ayni tel ayni yere gitsin
			Value: payloadBytes,
		})
		
		processedIDs = append(processedIDs, eventID)
	}

	rows.Close() // Silme işleminden önce satır okumasını kapat

	// ---> [KRİTİK DÜZELTME]: POISON PILL (Zehirli Hap) KONTROLÜ <---
	// Eğer Kafka'ya atılacak sağlam mesaj yoksa, AMA silinmesi gereken bozuk mesajlar varsa:
	if len(kafkaMessages) == 0 && len(processedIDs) > 0 {
		// Poison pill'leri direkt sil (Kafka'ya gönderilecek mesaj yok ama kuyruktan çıkmalılar)
		_, err = tx.Exec(ctx, "DELETE FROM outbox_events WHERE id = ANY($1::uuid[])", processedIDs)
		if err != nil {
			slog.Error("Zehirli outbox mesajlari silinemedi", "error", err)
			return 0
		}
		
		if err := tx.Commit(ctx); err != nil {
			slog.Error("Zehirli mesajlarin commit isleminde hata", "error", err)
			return 0
		}
		
		slog.Info("Paketteki tum mesajlar bozuktu (Poison Pill), hepsi cöpe atildi", "count", len(processedIDs))
		return len(processedIDs)
	}
	// Eğer gerçekten okunacak hiçbir şey yoksa (Kuyruk tamamen boşsa)
	if len(kafkaMessages) == 0 {
		return 0 
	}

	// 3. Kafka'ya TOPLU (Batch) Yazma
	if err := writer.WriteMessages(ctx, kafkaMessages...); err != nil {
		slog.Error("Kafka toplu yazma hatasi (DB Rollback yapilacak)", "error", err)
		return 0 // Kafka'ya gitmediyse, fonksiyondan çık. Defer sayesinde DB Rollback olur!
	}

	// 4. Kafka'ya başarıyla gittiyse, kilitli satırları veritabanından SİL
	if len(processedIDs) > 0 {
		_, err = tx.Exec(ctx, "DELETE FROM outbox_events WHERE id = ANY($1::uuid[])", processedIDs)
		if err != nil {
			slog.Error("Outbox silme hatasi", "error", err)
			return 0
		}
	}

	// 5. Her şey mükemmelse Transaction'ı Kapat (Commit)
	if err := tx.Commit(ctx); err != nil {
		slog.Error("Relay transaction commit hatasi", "error", err)
		return 0
	} 
	
	slog.Info("Outbox batch basariyla islendi", "count", len(processedIDs))
	return len(processedIDs)
}
