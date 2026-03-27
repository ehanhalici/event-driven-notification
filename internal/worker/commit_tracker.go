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
	mu sync.Mutex
	// partition → (offset → completed)
	inflight map[int]map[int64]bool
	reader   *kafka.Reader
	topic    string
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
	ct.inflight[msg.Partition][msg.Offset] = false
}

// MarkDone, Worker tarafından işlem bittikten sonra çağrılır.
// Mesajı tamamlanmış olarak işaretler ve watermark'a kadar commit yapar.
func (ct *CommitTracker) MarkDone(ctx context.Context, msg kafka.Message) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	partMap := ct.inflight[msg.Partition]
	if partMap == nil {
		return
	}
	partMap[msg.Offset] = true

	// Watermark hesapla: en düşük offset'ten başlayarak kesintisiz tamamlanmış en yüksek offset
	var offsets []int64
	for off := range partMap {
		offsets = append(offsets, off)
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })

	var watermark int64 = -1
	for _, off := range offsets {
		if !partMap[off] {
			break // tamamlanmamış mesaja ulaşıldı, dur
		}
		watermark = off
		delete(partMap, off) // commit edilecekler temizle
	}

	if watermark >= 0 {
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
		}
	}
}
