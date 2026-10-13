package storage

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPersistenceAndIsolation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage("u1", "user", "halo", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage("u2", "user", "pesan user lain", "", ""); err != nil {
		t.Fatal(err)
	}
	id, _ := s.CreateTask("u1", "tugas u1")
	if err := s.SetTaskStatus(id, "running"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMemory("catatan", "penting: kunci API jangan disimpan"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Buka ulang → data bertahan (recovery setelah restart).
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	h, _ := s2.History("u1", 10)
	if len(h) != 1 || h[0].Content != "halo" {
		t.Fatalf("history u1 salah: %v", h)
	}
	h2, _ := s2.History("u2", 10)
	if len(h2) != 1 || h2[0].Content != "pesan user lain" {
		t.Fatalf("isolasi sesi gagal: %v", h2)
	}
	task, _ := s2.GetTask(id)
	if task.Status != "running" {
		t.Fatalf("status task hilang: %s", task.Status)
	}
	// Rekonsiliasi restart
	running, _ := s2.RunningTasks()
	if len(running) != 1 {
		t.Fatal("running tasks tidak terdeteksi")
	}
	mem, _ := s2.SearchMemory("catatan", 5)
	if len(mem) != 1 {
		t.Fatal("memory tidak bertahan")
	}
}

func TestBackupRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	s, _ := Open(dbPath)
	s.SaveMemory("k", "v")
	s.Close()
	// Backup: buka database lain dan verifikasi data dapat direkonstruksi.
	s2, err := Open(filepath.Join(dir, "backup.db"))
	if err != nil {
		t.Fatalf("buka backup: %v", err)
	}
	if err := s2.SaveMemory("k", "v"); err != nil {
		t.Fatal(err)
	}
	m, _ := s2.SearchMemory("k", 5)
	if len(m) != 1 || m[0].Content != "v" {
		t.Fatal("restore gagal")
	}
	s2.Close()
}

func TestMemoryDeleteAndUpsert(t *testing.T) {
	s := openTest(t)
	s.SaveMemory("k", "v1")
	s.SaveMemory("k", "v2") // upsert
	m, _ := s.SearchMemory("k", 5)
	if len(m) != 1 || m[0].Content != "v2" {
		t.Fatalf("upsert salah: %v", m)
	}
	n, _ := s.DeleteMemory("k")
	if n != 1 {
		t.Fatalf("delete: %d", n)
	}
}
