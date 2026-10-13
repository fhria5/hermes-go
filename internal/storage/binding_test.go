package storage

import (
	"testing"
)

// Helper: buat approval pending dengan binding, lalu approve.
func mkApproval(t *testing.T, s *Store, id string, taskID int64, tool, bindHash string) {
	t.Helper()
	a := &Approval{ID: id, TaskID: taskID, SessionID: "tg:1", Tool: tool,
		ArgsHash: bindHash, ArgsPreview: "{}", CanonicalArgs: "{}",
		RequestedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := s.CreateApproval(a); err != nil {
		t.Fatal(err)
	}
	now := "2026-01-01T00:01:00Z"
	if ok, err := s.DecideApproval(id, true, 42, now); err != nil || !ok {
		t.Fatalf("approve gagal: %v %v", ok, err)
	}
}

// Uji: eksekusi sah — consume pertama sukses, kedua (replay) gagal.
func TestConsumeApprovedOnce(t *testing.T) {
	s := openTest(t)
	tid, _ := s.CreateTask("tg:1", "t")
	mkApproval(t, s, "c1", tid, "write_file", "HASH1")
	now := "2026-01-01T00:02:00Z"
	ok, err := s.ConsumeApprovedApproval("c1", tid, "write_file", "HASH1", now)
	if err != nil || !ok {
		t.Fatalf("consume pertama gagal: %v %v", ok, err)
	}
	// Replay callback / eksekusi kedua → ditolak.
	ok2, _ := s.ConsumeApprovedApproval("c1", tid, "write_file", "HASH1", now)
	if ok2 {
		t.Fatal("approval dieksekusi dua kali!")
	}
	a, _ := s.GetApprovalForExec("c1", now)
	if a.Status != "executed" {
		t.Fatalf("status=%s, harus executed", a.Status)
	}
}

// Uji: argumen berubah setelah approval (hash beda) → eksekusi ditolak.
func TestBindingArgsChanged(t *testing.T) {
	s := openTest(t)
	tid, _ := s.CreateTask("tg:1", "t")
	mkApproval(t, s, "c2", tid, "write_file", "HASH_ORIGINAL")
	ok, _ := s.ConsumeApprovedApproval("c2", tid, "write_file", "HASH_BARU", "2026-01-01T00:02:00Z")
	if ok {
		t.Fatal("argumen berubah tetap dieksekusi!")
	}
}

// Uji: tool name berubah → ditolak.
func TestBindingToolChanged(t *testing.T) {
	s := openTest(t)
	tid, _ := s.CreateTask("tg:1", "t")
	mkApproval(t, s, "c3", tid, "write_file", "HASH_ORIGINAL")
	ok, _ := s.ConsumeApprovedApproval("c3", tid, "shell", "HASH_ORIGINAL", "2026-01-01T00:02:00Z")
	if ok {
		t.Fatal("tool berubah tetap dieksekusi!")
	}
}

// Uji: task ID berubah → ditolak.
func TestBindingTaskChanged(t *testing.T) {
	s := openTest(t)
	tid, _ := s.CreateTask("tg:1", "t")
	mkApproval(t, s, "c4", tid, "write_file", "HASH_ORIGINAL")
	ok, _ := s.ConsumeApprovedApproval("c4", tid+999, "write_file", "HASH_ORIGINAL", "2026-01-01T00:02:00Z")
	if ok {
		t.Fatal("task berubah tetap dieksekusi!")
	}
}

// Uji: belum di-approve (masih pending) → consume ditolak.
func TestConsumeBeforeApproval(t *testing.T) {
	s := openTest(t)
	tid, _ := s.CreateTask("tg:1", "t")
	a := &Approval{ID: "c5", TaskID: tid, SessionID: "tg:1", Tool: "shell",
		ArgsHash: "H", ArgsPreview: "{}", CanonicalArgs: "{}",
		RequestedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2099-01-01T00:00:00Z"}
	_ = s.CreateApproval(a)
	ok, _ := s.ConsumeApprovedApproval("c5", tid, "shell", "H", "2026-01-01T00:01:00Z")
	if ok {
		t.Fatal("approval pending berhasil dikonsumsi!")
	}
}

// Uji: concurrent consume — hanya satu goroutine yang menang.
func TestConsumeConcurrent(t *testing.T) {
	s := openTest(t)
	tid, _ := s.CreateTask("tg:1", "t")
	mkApproval(t, s, "c6", tid, "shell", "H6")
	const n = 10
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() {
			ok, _ := s.ConsumeApprovedApproval("c6", tid, "shell", "H6", "2026-01-01T00:02:00Z")
			results <- ok
		}()
	}
	winners := 0
	for i := 0; i < n; i++ {
		if <-results {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("pemenang consume=%d, harus tepat 1", winners)
	}
}

// Uji: restart — approval yang belum dieksekusi tetap 'approved' di DB
// (persisten), tapi consume tetap tunduk pada binding.
func TestApprovalPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/t.db"
	s, _ := Open(dbPath)
	tid, _ := s.CreateTask("tg:1", "t")
	mkApproval(t, s, "c7", tid, "shell", "H7")
	s.Close()
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ok, err := s2.ConsumeApprovedApproval("c7", tid, "shell", "H7", "2026-01-01T00:03:00Z")
	if err != nil || !ok {
		t.Fatalf("approval hilang setelah restart: %v %v", ok, err)
	}
}
