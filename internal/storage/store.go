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
