package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

// newTestTracker, test icin minimal CommitTracker olusturur (reader olmadan).
// Track ve inflight state testleri icin kullanilir.
func newTestTracker() *CommitTracker {
	return &CommitTracker{
		inflight: make(map[int]map[int64]bool),
	}
}

// --- Track Testleri ---

func TestTrack_AddsToInflight(t *testing.T) {
	ct := newTestTracker()

	msg := kafka.Message{Partition: 0, Offset: 5}
	ct.Track(msg)

	if _, ok := ct.inflight[0]; !ok {
		t.Fatal("Partition 0 inflight haritasinda olmali")
	}
	if processed, ok := ct.inflight[0][5]; !ok || processed {
		t.Error("Offset 5, islenmemis (false) olarak kaydedilmeli")
	}
}

func TestTrack_MultiplePartitions(t *testing.T) {
	ct := newTestTracker()

	ct.Track(kafka.Message{Partition: 0, Offset: 1})
	ct.Track(kafka.Message{Partition: 1, Offset: 1})
	ct.Track(kafka.Message{Partition: 0, Offset: 2})
	ct.Track(kafka.Message{Partition: 2, Offset: 5})

	if len(ct.inflight) != 3 {
		t.Errorf("Beklenen 3 partition, alinan %d", len(ct.inflight))
	}
	if len(ct.inflight[0]) != 2 {
		t.Errorf("Partition 0'da beklenen 2 offset, alinan %d", len(ct.inflight[0]))
	}
	if len(ct.inflight[1]) != 1 {
		t.Errorf("Partition 1'de beklenen 1 offset, alinan %d", len(ct.inflight[1]))
	}
	if len(ct.inflight[2]) != 1 {
		t.Errorf("Partition 2'de beklenen 1 offset, alinan %d", len(ct.inflight[2]))
	}
}

func TestTrack_ConsecutiveOffsets(t *testing.T) {
	ct := newTestTracker()

	for i := int64(0); i < 5; i++ {
		ct.Track(kafka.Message{Partition: 0, Offset: i})
	}

	if len(ct.inflight[0]) != 5 {
		t.Errorf("Beklenen 5 offset, alinan %d", len(ct.inflight[0]))
	}

	for i := int64(0); i < 5; i++ {
		if ct.inflight[0][i] != false {
			t.Errorf("Offset %d islenmemis (false) olmali", i)
		}
	}
}

func TestTrack_ConcurrentAccess(t *testing.T) {
	ct := newTestTracker()
	var wg sync.WaitGroup

	// 100 goroutine'den eşzamanlı Track çağrısı
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			ct.Track(kafka.Message{Partition: 0, Offset: offset})
		}(int64(i))
	}
	wg.Wait()

	ct.mu.Lock()
	defer ct.mu.Unlock()
	if len(ct.inflight[0]) != 100 {
		t.Errorf("Beklenen 100 offset, alinan %d", len(ct.inflight[0]))
	}
}

// --- MarkDone & Watermark Testleri ---
// Bu testler Kafka broker'a ihtiyaç duyar (CommitMessages çağrısı nedeniyle).
// Sahte broker adresiyle reader oluşturulur; commit başarısız olur ama watermark mantığı test edilir.
// Context timeout ile CommitMessages'ın takılmasını engelliyoruz.

func newTestTrackerWithReader() *CommitTracker {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{"localhost:19999"}, // Sahte adres, bağlantı denenemez
		Topic:   "test-topic",
		GroupID: "test-group",
	})
	return NewCommitTracker(reader, "test-topic")
}

// shortCtx, CommitMessages'ın sahte broker'da takılmasını engelleyen kısa ömürlü context oluşturur.
func shortCtx() context.Context {
	ctx, _ := context.WithTimeout(context.Background(), 100*time.Millisecond)
	return ctx
}

func TestMarkDone_OutOfOrderDoesNotCommitEarly(t *testing.T) {
	ct := newTestTrackerWithReader()
	defer ct.reader.Close()

	// 3 mesaj takip et: 0, 1, 2
	ct.Track(kafka.Message{Partition: 0, Offset: 0})
	ct.Track(kafka.Message{Partition: 0, Offset: 1})
	ct.Track(kafka.Message{Partition: 0, Offset: 2})

	// Offset 2'yi önce tamamla (sıra dışı)
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 2})

	ct.mu.Lock()
	// Offset 0 ve 1 hala inflight'ta olmali (tamamlanmamış)
	if _, ok := ct.inflight[0][0]; !ok {
		t.Error("Offset 0 hala inflight'ta olmali")
	}
	if _, ok := ct.inflight[0][1]; !ok {
		t.Error("Offset 1 hala inflight'ta olmali")
	}
	// Offset 2 true olarak işaretlenmiş ama silinmemiş (watermark ilerleyemedi)
	if !ct.inflight[0][2] {
		t.Error("Offset 2 tamamlanmis (true) olarak isaretlenmeli")
	}
	ct.mu.Unlock()
}

func TestMarkDone_ContiguousCommit(t *testing.T) {
	ct := newTestTrackerWithReader()
	defer ct.reader.Close()

	// 3 mesaj takip et: 0, 1, 2
	ct.Track(kafka.Message{Partition: 0, Offset: 0})
	ct.Track(kafka.Message{Partition: 0, Offset: 1})
	ct.Track(kafka.Message{Partition: 0, Offset: 2})

	// Sirayla tamamla: 0, 1, 2
	// CommitMessages sahte broker'da basarisiz olacak ama watermark mantigi test edilir
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 0})
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 1})
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 2})

	ct.mu.Lock()
	// Tüm offset'ler temizlenmis olmali (commit edildi)
	if len(ct.inflight[0]) != 0 {
		t.Errorf("Tum offset'ler commit edilip temizlenmeli, kalan: %d", len(ct.inflight[0]))
	}
	ct.mu.Unlock()
}

func TestMarkDone_GapBlocksWatermark(t *testing.T) {
	ct := newTestTrackerWithReader()
	defer ct.reader.Close()

	// 5 mesaj takip et: 0, 1, 2, 3, 4
	for i := int64(0); i < 5; i++ {
		ct.Track(kafka.Message{Partition: 0, Offset: i})
	}

	// Offset 0, 2, 3, 4 tamamla (1 eksik → boşluk)
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 0})
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 2})
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 3})
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 4})

	ct.mu.Lock()
	// Offset 0 commit edilmis olmali (silinmis)
	if _, ok := ct.inflight[0][0]; ok {
		t.Error("Offset 0 commit edilip silinmis olmali")
	}
	// Offset 1 hala inflight'ta (false olarak)
	if processed, ok := ct.inflight[0][1]; !ok || processed {
		t.Error("Offset 1 inflight'ta ve islenmemis olmali")
	}
	// Offset 2, 3, 4 hala inflight'ta (true ama commit edilemez, 1 engelliyor)
	for _, off := range []int64{2, 3, 4} {
		if !ct.inflight[0][off] {
			t.Errorf("Offset %d tamamlanmis (true) olmali ama commit edilememeli", off)
		}
	}
	ct.mu.Unlock()

	// Simdi offset 1'i tamamla → watermark 4'e kadar ilerlemeli
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 1})

	ct.mu.Lock()
	if len(ct.inflight[0]) != 0 {
		t.Errorf("Tum offset'ler commit edilip temizlenmeli, kalan: %d", len(ct.inflight[0]))
	}
	ct.mu.Unlock()
}

func TestMarkDone_NilPartitionMap(t *testing.T) {
	ct := newTestTrackerWithReader()
	defer ct.reader.Close()

	// Track etmeden MarkDone çağrısı → panic olmamalı
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 99, Offset: 0})
	// Hata yoksa test basarili
}

func TestMarkDone_MultiplePartitionsIndependent(t *testing.T) {
	ct := newTestTrackerWithReader()
	defer ct.reader.Close()

	// İki ayrı partition'da mesaj takip et
	ct.Track(kafka.Message{Partition: 0, Offset: 0})
	ct.Track(kafka.Message{Partition: 0, Offset: 1})
	ct.Track(kafka.Message{Partition: 1, Offset: 0})
	ct.Track(kafka.Message{Partition: 1, Offset: 1})

	// Partition 0'da sadece offset 0'ı tamamla
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: 0})

	// Partition 1'de her ikisini de tamamla
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 1, Offset: 0})
	ct.MarkDone(shortCtx(), kafka.Message{Partition: 1, Offset: 1})

	ct.mu.Lock()
	// Partition 0: offset 0 silinmis, 1 kalmis
	if _, ok := ct.inflight[0][0]; ok {
		t.Error("Partition 0, offset 0 commit edilip silinmis olmali")
	}
	if _, ok := ct.inflight[0][1]; !ok {
		t.Error("Partition 0, offset 1 hala inflight'ta olmali")
	}

	// Partition 1: her ikisi de silinmis
	if len(ct.inflight[1]) != 0 {
		t.Errorf("Partition 1 tamamen temizlenmeli, kalan: %d", len(ct.inflight[1]))
	}
	ct.mu.Unlock()
}

func TestNewCommitTracker(t *testing.T) {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{"localhost:19999"},
		Topic:   "test-topic",
		GroupID: "test-group",
	})
	defer reader.Close()

	ct := NewCommitTracker(reader, "test-topic")

	if ct.reader != reader {
		t.Error("Reader doğru atanmamış")
	}
	if ct.topic != "test-topic" {
		t.Errorf("Topic beklenen 'test-topic', alinan '%s'", ct.topic)
	}
	if ct.inflight == nil {
		t.Error("inflight haritasi nil olmamali")
	}
}

func TestMarkDone_ConcurrentAccess(t *testing.T) {
	ct := newTestTrackerWithReader()
	defer ct.reader.Close()

	// 50 mesaj takip et
	for i := int64(0); i < 50; i++ {
		ct.Track(kafka.Message{Partition: 0, Offset: i})
	}

	// Tümünü eşzamanlı tamamla
	var wg sync.WaitGroup
	for i := int64(0); i < 50; i++ {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			ct.MarkDone(shortCtx(), kafka.Message{Partition: 0, Offset: offset})
		}(i)
	}
	wg.Wait()

	ct.mu.Lock()
	if len(ct.inflight[0]) != 0 {
		t.Errorf("Tum concurrent MarkDone sonrası inflight bos olmali, kalan: %d", len(ct.inflight[0]))
	}
	ct.mu.Unlock()
}
