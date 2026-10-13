package storage

import (
	"path/filepath"
	"testing"
)

// Uji recovery: restart saat tugas running → terdeteksi oleh RunningTasks (untuk rekonsiliasi).
func TestRestartRunningTaskDetection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	s, _ := Open(dbPath)
	id, _ := s.CreateTask("tg:1", "tugas panjang")
	_ = s.SetTaskStatus(id, "running")
	s.Close() // "crash"
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	running, _ := s2.RunningTasks()
	if len(running) != 1 || running[0].ID != id {
		t.Fatal("tugas running tidak terdeteksi untuk rekonsiliasi")
	}
	// Rekonsiliasi: fail, bukan eksekusi ulang.
	_ = s2.SetTaskStatus(id, "failed")
	t2, _ := s2.GetTask(id)
	if t2.Status != "failed" {
		t.Fatal("rekonsiliasi gagal")
	}
}

// Uji recovery: approval pending saat restart → ExpireApprovals di startup menangani.
func TestRestartPendingApproval(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	s, _ := Open(dbPath)
	tid, _ := s.CreateTask("tg:1", "t")
	_ = s.CreateApproval(&Approval{ID: "p1", TaskID: tid, SessionID: "tg:1", Tool: "shell",
		ArgsHash: "h", ArgsPreview: "{}", CanonicalArgs: "{}",
		RequestedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2030-01-01T00:00:00Z"})
	s.Close()
	s2, _ := Open(dbPath)
	defer s2.Close()
	// Startup worker: expire semua pending (fail-closed).
	if err := s2.ExpireApprovals("2099-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// Callback replay dari sesi sebelum restart → ditolak.
	if ok, _ := s2.DecideApproval("p1", true, 1, "2099-01-01T00:01:00Z"); ok {
		t.Fatal("approval pending dari crash tetap bisa diputus setelah restart!")
	}
}

// Uji backup/restore penuh: data → backup file → restore ke DB baru → data sama.
func TestBackupRestoreFullCycle(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "t.db")
	s, _ := Open(orig)
	tid, _ := s.CreateTask("tg:1", "penting")
	_ = s.SaveMemory("kunci", "nilai-penting")
	s.Close()
	// "Backup" = copy file (sudah checkpoint WAL saat Close).
	backup := filepath.Join(dir, "backup.db")
	if err := copyFile(orig, backup); err != nil {
		t.Fatal(err)
	}
	// "Restore" ke lokasi production.
	restored := filepath.Join(dir, "restored.db")
	if err := copyFile(backup, restored); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	task, err := s2.GetTask(tid)
	if err != nil || task.Description != "penting" {
		t.Fatal("task hilang setelah restore")
	}
	m, _ := s2.SearchMemory("kunci", 5)
	if len(m) != 1 || m[0].Content != "nilai-penting" {
		t.Fatal("memory hilang setelah restore")
	}
}

func copyFile(src, dst string) error {
	b, err := readFileAll(src)
	if err != nil {
		return err
	}
	return writeFileAll(dst, b)
}
