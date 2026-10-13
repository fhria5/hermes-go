package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// MemoryStore adalah interface kecil agar tools tidak bergantung pada package storage.
type MemoryStore interface {
	SaveMemory(key, content string) error
	SearchMemory(query string, limit int) ([]MemHit, error)
	DeleteMemory(key string) (int64, error)
}

type MemHit struct {
	Key     string
	Content string
}

type SearchMemoryTool struct{ Store MemoryStore }

func (t *SearchMemoryTool) Name() string { return "search_memory" }
func (t *SearchMemoryTool) Description() string {
	return "Cari memory jangka panjang berdasar kata kunci (pencarian teks SQL, bukan semantic search)."
}
func (t *SearchMemoryTool) Permission() Permission { return PermAuto }
func (t *SearchMemoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`)
}

func (t *SearchMemoryTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	hits, err := t.Store.SearchMemory(a.Query, 20)
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(hits)
	return Result{Output: string(out)}, nil
}

type SaveMemoryTool struct{ Store MemoryStore }

func (t *SaveMemoryTool) Name() string { return "save_memory" }
func (t *SaveMemoryTool) Description() string {
	return "Simpan fakta penting ke memory jangka panjang (key, content)."
}
func (t *SaveMemoryTool) Permission() Permission { return PermAuto }
func (t *SaveMemoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["key","content"],"properties":{"key":{"type":"string"},"content":{"type":"string"}}}`)
}

func (t *SaveMemoryTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Key     string `json:"key"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	if len(a.Content) > 10_000 {
		return Result{}, fmt.Errorf("memory terlalu besar (maks 10000 karakter)")
	}
	if err := t.Store.SaveMemory(a.Key, a.Content); err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("tersimpan: %s", a.Key)}, nil
}

// TaskStore untuk status & cancel task.
type TaskStore interface {
	GetTask(id int64) (*TaskRow, error)
	SetTaskStatus(id int64, status string) error
}

type TaskRow struct {
	ID          int64
	Status      string
	Description string
}

type TaskStatusTool struct {
	Store TaskStore
	Tasks *TaskTable // sinkronisasi cancel
}

func (t *TaskStatusTool) Name() string           { return "task_status" }
func (t *TaskStatusTool) Description() string    { return "Lihat status tugas berdasarkan ID." }
func (t *TaskStatusTool) Permission() Permission { return PermAuto }
func (t *TaskStatusTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`)
}

func (t *TaskStatusTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	task, err := t.Store.GetTask(a.ID)
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(task)
	return Result{Output: string(out)}, nil
}

type TaskCancelTool struct{ Tasks *TaskTable }

func (t *TaskCancelTool) Name() string { return "task_cancel" }
func (t *TaskCancelTool) Description() string {
	return "Batalkan tugas yang sedang berjalan berdasarkan ID."
}
func (t *TaskCancelTool) Permission() Permission { return PermAuto }
func (t *TaskCancelTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`)
}

func (t *TaskCancelTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	ok := t.Tasks.Cancel(a.ID)
	if !ok {
		return Result{}, fmt.Errorf("tugas %d tidak ditemukan atau tidak sedang berjalan", a.ID)
	}
	return Result{Output: fmt.Sprintf("tugas %d dibatalkan", a.ID)}, nil
}

// TaskTable: registry tugas aktif → context cancel (di dalam package tools agar
// tool task_cancel bisa membatalkan loop yang jalan di agent package tanpa import cycle).
type TaskTable struct {
	mu     sync.Mutex
	active map[int64]context.CancelFunc
}

func NewTaskTable() *TaskTable { return &TaskTable{active: map[int64]context.CancelFunc{}} }

func (tt *TaskTable) Register(id int64, cancel context.CancelFunc) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.active[id] = cancel
}
func (tt *TaskTable) Unregister(id int64) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	delete(tt.active, id)
}
func (tt *TaskTable) Cancel(id int64) bool {
	tt.mu.Lock()
	c, ok := tt.active[id]
	if ok {
		delete(tt.active, id)
	}
	tt.mu.Unlock()
	if !ok {
		return false
	}
	c()
	return true
}
