package worker

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/segmentio/kafka-go"
)

// CommitTracker, paralel worker havuzlarında güvenli offset commit sağlar.
// Sorun: Worker A (offset 5) ve Worker B (offset 10) aynı anda çalışırken,
// B önce biterse CommitMessages(offset=10) çağrısı Kafka'da offset ≤ 10'u
// "işlenmiş" olarak işaretler — A henüz bitmemiş olsa bile.
//
// Çözüm: Watermark (Filigran) tabanlı commit. Sadece tüm önceki offset'ler
// tamamlandığında commit yapılır.
// Örnek: offset 5, 6, 7 in-flight. 7 biter → commit yok. 5 biter → commit yok (6 hala bekliyor).
// 6 biter → 5,6,7 hepsi tamam → offset 7 commit edilir.
type CommitTracker struct {
	mu         sync.RWMutex
	inflight   map[int]map[int64]bool
	reader     *kafka.Reader
	topic      string
	isPoisoned bool
	poisonErr  error
}

// NewCommitTracker, belirtilen reader ve topic için yeni bir tracker oluşturur.
func NewCommitTracker(reader *kafka.Reader, topic string) *CommitTracker {
	return &CommitTracker{
		inflight: make(map[int]map[int64]bool),
		reader:   reader,
		topic:    topic,
	}
}

// Track, Fetcher tarafından çağrılır. Mesajı in-flight (henüz işlenmemiş) olarak kaydeder.
// Worker'lara dispatch etmeden ÖNCE çağrılmalıdır.
func (ct *CommitTracker) Track(msg kafka.Message) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	
	if ct.inflight[msg.Partition] == nil {
		ct.inflight[msg.Partition] = make(map[int64]bool)
	}
	// Başlangıçta mesajı in-flight (tamamlanmamış) olarak işaretle
	ct.inflight[msg.Partition][msg.Offset] = false
}

// MarkDone, Worker tarafından işlem bittikten sonra çağrılır.
// Mesajı tamamlanmış olarak işaretler ve watermark'a kadar commit yapar.
func (ct *CommitTracker) MarkDone(ctx context.Context, msg kafka.Message) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// Eğer tracker (altyapı hatası yüzünden) zehirlendiyse, pod zaten kapanma sürecindedir.
	// Arkadan gelen başarılı worker'ların state'i bozmasını veya gereksiz log/commit 
	// denemesi yapmasını engellemek için anında işlemi reddediyoruz.
	if ct.isPoisoned {
		slog.WarnContext(ctx, "Tracker zehirlendi (Kapanis devrede), commit atlandi",
			"topic", ct.topic,
			"partition", msg.Partition,
			"offset", msg.Offset,
			"poison_error", ct.poisonErr, // Hangi hatadan zehirlendiğini de loga ekliyoruz
		)
		return
	}
	
	partMap := ct.inflight[msg.Partition]
	if partMap == nil {
		return
	}
	
	// Mesajın işlendiğini işaretle
	partMap[msg.Offset] = true

	// Watermark hesapla: en düşük offset'ten başlayarak kesintisiz tamamlanmış en yüksek offset
	var offsets []int64
	for off := range partMap {
		offsets = append(offsets, off)
	}
	// Küçükten büyüğe sırala
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })

	var watermark int64 = -1
	var toDelete []int64 // ---> [KRİTİK DÜZELTME]: Silinecek offset'leri geçici olarak tut

	for _, off := range offsets {
		if !partMap[off] {
			break // Tamamlanmamış mesaja ulaşıldı, zincir kırıldı, dur.
		}
		watermark = off
		toDelete = append(toDelete, off) // Silinecekler listesine ekle
	}

	if watermark >= 0 {
		// Kafka'ya Commit at
		if err := ct.reader.CommitMessages(ctx, kafka.Message{
			Topic:     ct.topic,
			Partition: msg.Partition,
			Offset:    watermark,
		}); err != nil {
			slog.ErrorContext(ctx, "Watermark commit hatasi",
				"topic", ct.topic,
				"partition", msg.Partition,
				"offset", watermark,
				"error", err,
			)
			// Hata varsa silme işlemini yapmadan dön!
			// Bu sayede offset'ler in-flight map'inde kalır ve sonraki MarkDone
			// çağrısında yeniden commit edilmeyi denerler.
			return
		}

		// SADECE Commit başarılı olursa haritadan sil
		for _, off := range toDelete {
			delete(partMap, off)
		}
	}
}

// MarkPoisoned, kritik bir altyapı hatası (örn: DB çökmesi) yaşandığında
// tracker'ı zehirli (kullanılamaz) olarak işaretler. Bu sayede arkadan gelen
// diğer başarılı worker'ların yanlışlıkla out-of-order commit yapmasını engeller.
func (ct *CommitTracker) MarkPoisoned(err error) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// Eğer zaten zehirlendiyse ilk hatayı ezmemek için kontrol ediyoruz
	if !ct.isPoisoned {
		ct.isPoisoned = true
		ct.poisonErr = err
	}
}

// IsPoisoned: İşlemlerin başında tracker'ın durumunu kontrol etmek için
func (ct *CommitTracker) IsPoisoned() bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.isPoisoned
}
