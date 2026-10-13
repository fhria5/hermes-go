package storage

import (
	"testing"
	"time"
)

func timeParseUTC(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }

type time2 = time.Time

const timeSecond = time.Second
const timeRFC3339Nano = time.RFC3339Nano

// Uji: approval sah → applied; double-click → kedua kalinya gagal (atomik).
func TestApprovalLifecycle(t *testing.T) {
	s := openTest(t)
	// task_id=0 tidak ada di tasks → test FK dengan task sungguhan.
	tid, _ := s.CreateTask("tg:1", "t")
	a := &Approval{ID: "a1", TaskID: tid, SessionID: "tg:1", Tool: "shell",
		ArgsHash: "h", ArgsPreview: "{}", RequestedAt: "2026-01-01T00:00:00Z",
		ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := s.CreateApproval(a); err != nil {
		t.Fatal(err)
	}
	now := "2026-01-01T00:01:00Z"
	ok, err := s.DecideApproval("a1", true, 42, now)
	if err != nil || !ok {
		t.Fatalf("decide pertama: applied=%v err=%v", ok, err)
	}
	// Double-click: kedua kalinya harus gagal.
	ok2, _ := s.DecideApproval("a1", true, 42, now)
	if ok2 {
		t.Fatal("approval diproses dua kali!")
	}
	// GetApproval setelah diproses → error.
	if _, err := s.GetApproval("a1", now); err == nil {
		t.Fatal("approval sudah diproses masih bisa diambil")
	}
}

// Uji: kedaluwarsa → tidak dapat disetujui.
func TestApprovalExpired(t *testing.T) {
	s := openTest(t)
	a := &Approval{ID: "a2", TaskID: 1, SessionID: "tg:1", Tool: "shell",
		ArgsHash: "h", ArgsPreview: "{}", RequestedAt: "2026-01-01T00:00:00Z",
		ExpiresAt: "2026-01-01T00:05:00Z"}
	_ = s.CreateApproval(a)
	now := "2026-01-01T00:10:00Z" // lewat kedaluwarsa
	if ok, _ := s.DecideApproval("a2", true, 1, now); ok {
		t.Fatal("approval kedaluwarsa berhasil disetujui!")
	}
	if _, err := s.GetApproval("a2", now); err == nil {
		t.Fatal("approval kedaluwarsa masih bisa diambil")
	}
}

// Uji: ExpireApprovals menandai massal saat startup.
func TestExpireApprovals(t *testing.T) {
	s := openTest(t)
	_ = s.CreateApproval(&Approval{ID: "e1", TaskID: 1, SessionID: "tg:1", Tool: "shell",
		ArgsHash: "h", ArgsPreview: "{}", RequestedAt: "2026-01-01T00:00:00Z",
		ExpiresAt: "2026-01-01T00:01:00Z"})
	if err := s.ExpireApprovals("2026-01-01T00:02:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetApproval("e1", "2026-01-01T00:02:00Z"); err == nil {
		t.Fatal("approval belum dipindahkan ke expired")
	}
}

// Uji: ClearSession hanya menghapus sesi yang dituju (isolasi antar user).
func TestClearSessionIsolation(t *testing.T) {
	s := openTest(t)
	_ = s.AppendMessage("tg:1", "user", "milik user 1", "", "")
	_ = s.AppendMessage("tg:2", "user", "milik user 2", "", "")
	if err := s.ClearSession("tg:1"); err != nil {
		t.Fatal(err)
	}
	h1, _ := s.History("tg:1", 10)
	if len(h1) != 0 {
		t.Fatal("sesi tg:1 tidak terhapus")
	}
	h2, _ := s.History("tg:2", 10)
	if len(h2) != 1 {
		t.Fatal("sesi tg:2 ikut terhapus — isolasi rusak!")
	}
}

// Uji: scheduler — due & anti-duplikasi (dua tick berdekatan tidak menjalankan dua kali).
func TestDueSchedulesAtomic(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateSchedule(&Schedule{SessionID: "tg:1", Name: "j1", Prompt: "p",
		Kind: "interval", IntervalSec: 60, TZ: "UTC", Enabled: true,
		NextRunAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	_ = id
	now := timeParseUTC("2026-01-01T00:01:00Z")
	due, err := s.DueSchedules(now, func(sc Schedule, n time.Time) string {
		return n.Add(60 * time.Second).Format(time.RFC3339Nano)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("due=%d, harus 1", len(due))
	}
	// Tick kedua pada waktu sama → tidak boleh due lagi (next sudah digeser).
	due2, _ := s.DueSchedules(now, func(sc Schedule, n time.Time) string {
		return n.Add(60 * time.Second).Format(time.RFC3339Nano)
	})
	if len(due2) != 0 {
		t.Fatalf("tick ganda: due=%d — job dijalankan dua kali!", len(due2))
	}
}

// Uji: restart — due schedule tetap ada (persisten, bukan di memori).
func TestSchedulePersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/t.db"
	s, _ := Open(dbPath)
	_, err := s.CreateSchedule(&Schedule{SessionID: "tg:1", Name: "j", Prompt: "p",
		Kind: "interval", IntervalSec: 60, TZ: "UTC", Enabled: true,
		NextRunAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	due, _ := s2.DueSchedules(timeParseUTC("2026-01-01T00:02:00Z"), func(sc Schedule, n time.Time) string {
		return n.Add(60 * time.Second).Format(time.RFC3339Nano)
	})
	if len(due) != 1 {
		t.Fatal("jadwal hilang setelah restart")
	}
}
