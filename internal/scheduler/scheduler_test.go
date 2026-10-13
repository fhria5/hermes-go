package scheduler

import (
	"context"
	"testing"
	"time"

	"hermes-go/internal/storage"
)

func tContext() context.Context { return context.Background() }

func openTestSched(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Uji: missed-run interval → satu eksekusi, next dari now (bukan backlog).
func TestIntervalMissedRunPolicy(t *testing.T) {
	s := openTestSched(t)
	// next_run_at 3 hari lalu; interval 60 detik.
	_, err := s.CreateSchedule(&storage.Schedule{SessionID: "tg:1", Name: "j", Prompt: "p",
		Kind: "interval", IntervalSec: 60, TZ: "UTC", Enabled: true,
		NextRunAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 4, 0, 0, 30, 0, time.UTC)
	due, _ := s.DueSchedules(now, computeNext)
	if len(due) != 1 {
		t.Fatalf("due=%d, harus tepat 1 (bukan backlog 4320 eksekusi)", len(due))
	}
	list, _ := s.ListSchedules("tg:1")
	if list[0].NextRunAt != now.Add(60*time.Second).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("next=%s, harus now+60s", list[0].NextRunAt)
	}
}

// Uji: missed-run once > grace → nonaktif, bukan dijalankan.
func TestOnceMissedBeyondGrace(t *testing.T) {
	s := openTestSched(t)
	_, _ = s.CreateSchedule(&storage.Schedule{SessionID: "tg:1", Name: "o", Prompt: "p",
		Kind: "once", TZ: "UTC", Enabled: true,
		NextRunAt: "2025-12-01T00:00:00Z", CreatedAt: "2025-12-01T00:00:00Z"})
	ran := false
	sch := New(s, func(prompt, session string) { ran = true })
	sch.tick(tContext(), time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)) // >24 jam terlewat
	if ran {
		t.Fatal("once yang terlewat >24 jam tetap dijalankan!")
	}
	list, _ := s.ListSchedules("tg:1")
	if list[0].Enabled {
		t.Fatal("once terlewat harus dinonaktifkan")
	}
}

// Uji: once dalam grace → dijalankan sekali.
func TestOnceWithinGrace(t *testing.T) {
	s := openTestSched(t)
	_, _ = s.CreateSchedule(&storage.Schedule{SessionID: "tg:1", Name: "o", Prompt: "p",
		Kind: "once", TZ: "UTC", Enabled: true,
		NextRunAt: "2026-01-01T00:00:00Z", CreatedAt: "2025-12-31T23:00:00Z"})
	ran := 0
	sch := New(s, func(prompt, session string) { ran++ })
	sch.tick(tContext(), time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)) // 10 jam (dalam grace)
	if ran != 1 {
		t.Fatalf("ran=%d, harus 1", ran)
	}
	// next_run_at='' → tidak due lagi.
	due, _ := s.DueSchedules(time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC), computeNext)
	if len(due) != 0 {
		t.Fatal("once dijalankan dua kali")
	}
}

// Uji: disabled schedule tidak dijalankan.
func TestDisabledNotRun(t *testing.T) {
	s := openTestSched(t)
	id, _ := s.CreateSchedule(&storage.Schedule{SessionID: "tg:1", Name: "d", Prompt: "p",
		Kind: "interval", IntervalSec: 60, TZ: "UTC", Enabled: false,
		NextRunAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z"})
	_, _ = s.SetScheduleEnabled(id, "tg:1", false)
	ran := false
	New(s, func(prompt, session string) { ran = true }).tick(tContext(), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if ran {
		t.Fatal("jadwal disabled tetap dijalankan")
	}
}

// Uji: perubahan waktu sistem mundur — next_run_at di masa depan tidak due.
func TestClockSkewBackward(t *testing.T) {
	s := openTestSched(t)
	_, _ = s.CreateSchedule(&storage.Schedule{SessionID: "tg:1", Name: "k", Prompt: "p",
		Kind: "interval", IntervalSec: 3600, TZ: "UTC", Enabled: true,
		NextRunAt: "2026-01-01T01:00:00Z", CreatedAt: "2026-01-01T00:00:00Z"})
	ran := false
	New(s, func(prompt, session string) { ran = true }).tick(tContext(), time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC))
	if ran {
		t.Fatal("jadwal dieksekusi sebelum waktunya (jam mundur tidak ditangani)")
	}
}
