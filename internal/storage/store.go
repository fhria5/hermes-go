// Package storage: SQLite persisten — sesi, pesan, tugas, memory, tool call log.
package storage

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // hindari SQLITE_BUSY, cukup untuk beban ringan
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrasi: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	// Tabel versi dibuat lebih dulu agar pengecekan migrasi pertama tidak gagal.
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	// Nama file 0001_....sql menjamin urutan lexicographic.
	for _, e := range entries {
		name := e.Name()
		var applied int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, name).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrasi %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.DB.Close() }

// ---------- Pesan / sesi ----------

func (s *Store) AppendMessage(sessionID, role, content string, toolCallsJSON string, toolCallID string) error {
	_, err := s.DB.Exec(`INSERT INTO messages(session_id, role, content, tool_calls, tool_call_id, created_at)
		VALUES(?,?,?,?,?,?)`, sessionID, role, content, toolCallsJSON, toolCallID, time.Now().UTC())
	return err
}

func (s *Store) History(sessionID string, limit int) ([]Msg, error) {
	rows, err := s.DB.Query(`SELECT role, content, tool_calls, tool_call_id FROM (
		SELECT id, role, content, tool_calls, tool_call_id FROM messages
		WHERE session_id=? ORDER BY id DESC LIMIT ?) ORDER BY id ASC`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Msg
	for rows.Next() {
		var m Msg
		if err := rows.Scan(&m.Role, &m.Content, &m.ToolCalls, &m.ToolCallID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type Msg struct {
	Role, Content, ToolCalls, ToolCallID string
}

// ---------- Task ----------

func (s *Store) CreateTask(sessionID, description string) (int64, error) {
	r, err := s.DB.Exec(`INSERT INTO tasks(session_id, description, status, created_at, updated_at)
		VALUES(?,?,?,?,?)`, sessionID, description, "planned", time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) SetTaskStatus(id int64, status string) error {
	_, err := s.DB.Exec(`UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, time.Now().UTC(), id)
	return err
}

func (s *Store) GetTask(id int64) (*Task, error) {
	t := &Task{}
	err := s.DB.QueryRow(`SELECT id, session_id, description, status, COALESCE(result,''), created_at, updated_at
		FROM tasks WHERE id=?`, id).
		Scan(&t.ID, &t.SessionID, &t.Description, &t.Status, &t.Result, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) ListTasks(sessionID string, limit int) ([]Task, error) {
	rows, err := s.DB.Query(`SELECT id, status, description, created_at FROM tasks
		WHERE session_id=? ORDER BY id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Status, &t.Description, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RunningTasks mengembalikan tugas berstatus running/waiting_approval
// (untuk rekonsiliasi setelah restart — status akhir tidak diketahui).
func (s *Store) RunningTasks() ([]Task, error) {
	rows, err := s.DB.Query(`SELECT id, session_id, status, description FROM tasks
		WHERE status IN ('running','waiting_approval')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Status, &t.Description); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type Task struct {
	ID          int64
	SessionID   string
	Description string
	Status      string
	Result      string
	CreatedAt   string
	UpdatedAt   string
}

// ---------- Memory ----------

func (s *Store) SaveMemory(key, content string) error {
	_, err := s.DB.Exec(`INSERT INTO memory(key, content, updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET content=excluded.content, updated_at=excluded.updated_at`,
		key, content, time.Now().UTC())
	return err
}

func (s *Store) SearchMemory(query string, limit int) ([]MemoryEntry, error) {
	rows, err := s.DB.Query(`SELECT key, content, updated_at FROM memory
		WHERE key LIKE '%'||?||'%' OR content LIKE '%'||?||'%'
		ORDER BY updated_at DESC LIMIT ?`, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryEntry
	for rows.Next() {
		var m MemoryEntry
		if err := rows.Scan(&m.Key, &m.Content, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMemory(key string) (int64, error) {
	r, err := s.DB.Exec(`DELETE FROM memory WHERE key=?`, key)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

type MemoryEntry struct {
	Key       string
	Content   string
	UpdatedAt string
}

// ---------- Tool call log ----------

func (s *Store) LogToolCall(taskID int64, tool, args string, status string, dur time.Duration, errStr string) {
	_, _ = s.DB.Exec(`INSERT INTO tool_calls(task_id, tool, args, status, duration_ms, error, created_at)
		VALUES(?,?,?,?,?,?,?)`, taskID, tool, args, status, dur.Milliseconds(), errStr, time.Now().UTC())
}

func JSONString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------- Approvals ----------

type Approval struct {
	ID          string
	TaskID      int64
	SessionID   string
	Tool        string
	ArgsHash    string
	ArgsPreview string
	Status      string
	RequestedAt string
	ExpiresAt   string
	DecidedBy   int64
}

func (s *Store) CreateApproval(a *Approval) error {
	_, err := s.DB.Exec(`INSERT INTO approvals(id, task_id, session_id, tool, args_hash, args_preview, status, requested_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, a.TaskID, a.SessionID, a.Tool, a.ArgsHash, a.ArgsPreview, "pending", a.RequestedAt, a.ExpiresAt)
	return err
}

// DecideApproval mengubah status pending → approved/rejected secara atomik
// (UPDATE ... WHERE status='pending' — hanya satu caller yang menang).
// Kembalikan (applied, err): applied=false berarti sudah diproses/diubah orang lain.
func (s *Store) DecideApproval(id string, approve bool, by int64, now string) (bool, error) {
	st := "rejected"
	if approve {
		st = "approved"
	}
	// Kedaluwarsa dicek di sini juga: kedaluwarsa → tidak bisa disetujui.
	r, err := s.DB.Exec(`UPDATE approvals SET status=?, decided_at=?, decided_by=?
		WHERE id=? AND status='pending' AND expires_at > ?`, st, now, by, id, now)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

// GetApproval mengembalikan approval hanya jika masih pending dan belum kedaluwarsa.
func (s *Store) GetApproval(id, now string) (*Approval, error) {
	a := &Approval{}
	err := s.DB.QueryRow(`SELECT id, task_id, session_id, tool, args_hash, args_preview, status, COALESCE(decided_by,0), expires_at
		FROM approvals WHERE id=?`, id).
		Scan(&a.ID, &a.TaskID, &a.SessionID, &a.Tool, &a.ArgsHash, &a.ArgsPreview, &a.Status, &a.DecidedBy, &a.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if a.Status != "pending" || a.ExpiresAt <= now {
		return nil, fmt.Errorf("approval %s tidak dapat diproses (status=%s)", id, a.Status)
	}
	return a, nil
}

// ExpireApprovals menandai kedaluwarsa (dipanggil berkala dan saat startup).
func (s *Store) ExpireApprovals(now string) error {
	_, err := s.DB.Exec(`UPDATE approvals SET status='expired' WHERE status='pending' AND expires_at <= ?`, now)
	return err
}

// ---------- Schedules ----------

type Schedule struct {
	ID          int64
	SessionID   string
	Name        string
	Prompt      string
	Kind        string // once | interval
	RunAt       string // untuk once (RFC3339, dalam tz)
	IntervalSec int
	TZ          string
	Enabled     bool
	NextRunAt   string
	LastRunAt   string
	LastResult  string
	CreatedAt   string
}

const MaxSchedules = 50

func (s *Store) CreateSchedule(sc *Schedule) (int64, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schedules`).Scan(&n); err != nil {
		return 0, err
	}
	if n >= MaxSchedules {
		return 0, fmt.Errorf("batas %d jadwal terlampaui; hapus jadwal lama dulu", MaxSchedules)
	}
	r, err := s.DB.Exec(`INSERT INTO schedules(session_id, name, prompt, kind, run_at, interval_seconds, tz, enabled, next_run_at, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		sc.SessionID, sc.Name, sc.Prompt, sc.Kind, sc.RunAt, sc.IntervalSec, sc.TZ, boolInt(sc.Enabled), sc.NextRunAt, nowStr())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) ListSchedules(sessionID string) ([]Schedule, error) {
	rows, err := s.DB.Query(`SELECT id, name, prompt, kind, COALESCE(run_at,''), COALESCE(interval_seconds,0), tz, enabled, COALESCE(next_run_at,''), COALESCE(last_run_at,''), COALESCE(last_result,'')
		FROM schedules WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var en int
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Prompt, &sc.Kind, &sc.RunAt, &sc.IntervalSec, &sc.TZ, &en, &sc.NextRunAt, &sc.LastRunAt, &sc.LastResult); err != nil {
			return nil, err
		}
		sc.Enabled = en == 1
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) SetScheduleEnabled(id int64, sessionID string, enabled bool) (bool, error) {
	r, err := s.DB.Exec(`UPDATE schedules SET enabled=? WHERE id=? AND session_id=?`, boolInt(enabled), id, sessionID)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

func (s *Store) DeleteSchedule(id int64, sessionID string) (bool, error) {
	r, err := s.DB.Exec(`DELETE FROM schedules WHERE id=? AND session_id=?`, id, sessionID)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

// DueSchedules: jadwal enabled yang next_run_at <= now (ATOMIK: sekaligus
// menunda next_run_at dengan CAS-style WHERE next_run_at=old agar worker
// ganda tidak menjalankan job yang sama dua kali).
// computeNext menerima (schedule, now) — kebijakan missed-run ada di pemanggil.
func (s *Store) DueSchedules(now time.Time, computeNext func(sc Schedule, now time.Time) string) ([]Schedule, error) {
	nowS := now.UTC().Format(time.RFC3339Nano)
	rows, err := s.DB.Query(`SELECT id, session_id, name, prompt, kind, COALESCE(interval_seconds,0), tz, next_run_at
		FROM schedules WHERE enabled=1 AND next_run_at != '' AND next_run_at <= ?`, nowS)
	if err != nil {
		return nil, err
	}
	var due []Schedule
	for rows.Next() {
		var sc Schedule
		if err := rows.Scan(&sc.ID, &sc.SessionID, &sc.Name, &sc.Prompt, &sc.Kind, &sc.IntervalSec, &sc.TZ, &sc.NextRunAt); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, sc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, sc := range due {
		next := computeNext(sc, now)
		// CAS: update hanya jika next_run_at masih nilai lama.
		if _, err := s.DB.Exec(`UPDATE schedules SET next_run_at=?, last_run_at=? WHERE id=? AND next_run_at=?`,
			next, nowS, sc.ID, sc.NextRunAt); err != nil {
			return due, err
		}
	}
	return due, nil
}

func (s *Store) SetScheduleResult(id int64, result string) error {
	_, err := s.DB.Exec(`UPDATE schedules SET last_result=? WHERE id=?`, result, id)
	return err
}

func (s *Store) LogScheduleRun(scheduleID, taskID int64, status string) error {
	_, err := s.DB.Exec(`INSERT INTO schedule_runs(schedule_id, task_id, status, started_at) VALUES(?,?,?,?)`,
		scheduleID, taskID, status, nowStr())
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func nowStr() string { return time.Now().UTC().Format(time.RFC3339) }

// ClearSession menghapus seluruh riwayat pesan SATU sesi (transaksional).
// Memory jangka panjang, tugas, dan log tool call tidak terpengaruh.
func (s *Store) ClearSession(sessionID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id=?`, sessionID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
