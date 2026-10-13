package storage

import (
	"testing"
	"time"
)

func oldTS(daysAgo int) string {
	return time.Now().UTC().AddDate(0, 0, -daysAgo).Format(time.RFC3339)
}

// Uji: clock terkontrol — memory lama terhapus, baru tetap.
func TestRetentionExpiryAndNonExpiry(t *testing.T) {
	s := openTest(t)
	// Simulasi memory lama: insert langsung dengan updated_at tua.
	if _, err := s.DB.Exec(`INSERT INTO memory(key, content, updated_at) VALUES('lama','c',?)`, oldTS(120)); err != nil {
		t.Fatal(err)
	}
	_ = s.SaveMemory("baru", "fresh") // updated_at sekarang
	cutoff := oldTS(90)
	res, err := s.ApplyMemoryRetention(cutoff, 500)
	if err != nil {
		t.Fatal(err)
	}
	if res.MemoryDeleted != 1 {
		t.Fatalf("dihapus=%d, harus 1", res.MemoryDeleted)
	}
	n, _ := s.CountMemory()
	if n != 1 {
		t.Fatalf("sisa=%d, harus 1 (yang baru)", n)
	}
	hits, _ := s.SearchMemory("baru", 5)
	if len(hits) != 1 {
		t.Fatal("memory baru ikut terhapus!")
	}
}

// Uji: konfigurasi tidak valid → fail-safe, tidak menghapus.
func TestRetentionInvalidCutoff(t *testing.T) {
	s := openTest(t)
	_ = s.SaveMemory("k", "v")
	if _, err := s.ApplyMemoryRetention("bukan-tanggal", 500); err == nil {
		t.Fatal("cutoff tidak valid harus error")
	}
	res, err := s.ApplyMemoryRetention("", 500)
	if err != nil || res.MemoryDeleted != 0 {
		t.Fatal("cutoff kosong harus no-op")
	}
	n, _ := s.CountMemory()
	if n != 1 {
		t.Fatal("memory terhapus oleh retensi tidak valid!")
	}
}

// Uji: batch membatasi jumlah hapus per siklus.
func TestRetentionBatch(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 5; i++ {
		if _, err := s.DB.Exec(`INSERT INTO memory(key, content, updated_at) VALUES(?,?,?)`,
			"k"+string(rune('a'+i)), "c", oldTS(120)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.ApplyMemoryRetention(oldTS(90), 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.MemoryDeleted != 2 {
		t.Fatalf("batch tidak dihormati: dihapus=%d", res.MemoryDeleted)
	}
}

// Uji: retensi tidak menyentuh tugas, approval, dan pesan (isolasi kategori).
func TestRetentionCategoryIsolation(t *testing.T) {
	s := openTest(t)
	tid, _ := s.CreateTask("tg:1", "tugas penting")
	_ = s.SetTaskStatus(tid, "running")
	_ = s.AppendMessage("tg:1", "user", "pesan", "", "")
	_, _ = s.CreateSchedule(&Schedule{SessionID: "tg:1", Name: "j", Prompt: "p",
		Kind: "once", TZ: "UTC", Enabled: true, NextRunAt: "2099-01-01T00:00:00Z", CreatedAt: oldTS(200)})
	if _, err := s.DB.Exec(`INSERT INTO memory(key, content, updated_at) VALUES('m','c',?)`, oldTS(120)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyMemoryRetention(oldTS(90), 500); err != nil {
		t.Fatal(err)
	}
	// Task tetap ada.
	task, err := s.GetTask(tid)
	if err != nil || task.Status != "running" {
		t.Fatal("task ikut terhapus oleh retensi!")
	}
	// Pesan tetap ada.
	h, _ := s.History("tg:1", 10)
	if len(h) != 1 {
		t.Fatal("pesan sesi ikut terhapus!")
	}
	// Jadwal tetap ada.
	list, _ := s.ListSchedules("tg:1")
	if len(list) != 1 {
		t.Fatal("jadwal ikut terhapus!")
	}
}

// Uji: memory ditulis saat retensi berjalan tidak ikut terhapus (race-safe
// via timestamp: cutoff dihitung sebelum siklus; tulisan baru updated_at=now > cutoff).
func TestRetentionRaceNewWrite(t *testing.T) {
	s := openTest(t)
	cutoff := oldTS(90)
	// Tulis memory SETELAH cutoff dihitung (updated_at sekarang > cutoff).
	_ = s.SaveMemory("baru-saat-retensi", "v")
	res, err := s.ApplyMemoryRetention(cutoff, 500)
	if err != nil {
		t.Fatal(err)
	}
	if res.MemoryDeleted != 0 {
		t.Fatalf("memory baru ikut terhapus: %d", res.MemoryDeleted)
	}
}
