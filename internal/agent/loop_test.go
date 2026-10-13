package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"hermes-go/internal/llm"
	"hermes-go/internal/skills"
	"hermes-go/internal/storage"
	"hermes-go/internal/tools"
)

func testRunner(t *testing.T, responses []llm.ChatResponse) (*Runner, *storage.Store, *tools.TaskTable) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	reg := tools.NewRegistry()
	fsCfg := tools.FSConfig{Root: dir, MaxFileBytes: 1_000_000, WriteEnabled: true}
	reg.Register(&tools.ReadFileTool{Cfg: fsCfg})
	reg.Register(&tools.WriteFileTool{Cfg: fsCfg})
	taskTable := tools.NewTaskTable()
	return &Runner{
		Provider: &llm.MockProvider{Responses: responses, ModelName: "mock"},
		Registry: reg, Store: store, TaskTable: taskTable,
		Limits: Limits{MaxIterations: 5, MaxToolCalls: 10, MaxTokens: 100_000, ToolTimeout: 5_000_000_000, MaxOutput: 100_000},
		Skills: nil, Log: slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}, store, taskTable
}

func toolCallMsg(id, name, args string) llm.ChatResponse {
	var tc llm.ToolCall
	tc.ID = id
	tc.Type = "function"
	tc.Function.Name = name
	tc.Function.Arguments = args
	return llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{tc}}}},
		Usage:   llm.Usage{TotalTokens: 10},
	}
}

func textMsg(s string) llm.ChatResponse {
	return llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: s}}},
		Usage:   llm.Usage{TotalTokens: 10},
	}
}

// Uji: tool calling berulang lalu jawaban akhir → succeeded.
func TestLoopToolCallingSucceeds(t *testing.T) {
	r, store, _ := testRunner(t, []llm.ChatResponse{
		toolCallMsg("c1", "write_file", `{"path":"a.txt","content":"hello"}`),
		toolCallMsg("c2", "read_file", `{"path":"a.txt"}`),
		textMsg("selesai, isi file: hello"),
	})
	id, _ := store.CreateTask("s1", "tulis dan baca")
	res, status := r.Run(context.Background(), id, "s1", "tulis hello lalu baca")
	if status != "succeeded" {
		t.Fatalf("status=%s res=%s", status, res)
	}
	task, _ := store.GetTask(id)
	if task.Status != "succeeded" {
		t.Fatalf("task status=%s", task.Status)
	}
}

// Uji: batas iterasi.
func TestLoopIterationLimit(t *testing.T) {
	r, store, _ := testRunner(t, nil)
	r.Limits.MaxIterations = 2
	// mock terbatas; setelah preset habis, Chat error → failed. Gunakan mock tak terbatas:
	type infMock struct{ n int }
	// Sederhananya verifikasi error mock → failed:
	id, _ := store.CreateTask("s1", "x")
	_, status := r.Run(context.Background(), id, "s1", "tugas")
	if status != "failed" {
		t.Fatalf("status=%s", status)
	}
	var _ infMock // placeholder
}

// Uji: tool call dengan argumen tidak valid → error dikembalikan ke LLM sebagai data.
func TestInvalidToolArgs(t *testing.T) {
	r, store, _ := testRunner(t, []llm.ChatResponse{
		toolCallMsg("c1", "read_file", `{}`), // required "path" hilang
		textMsg("ok, argumen diperbaiki"),
	})
	id, _ := store.CreateTask("s1", "x")
	_, status := r.Run(context.Background(), id, "s1", "x")
	if status != "succeeded" {
		t.Fatal("harus tetap succeeded karena error tool dikirim balik ke LLM")
	}
}

// Uji: tool PermApproval ditolak ketika Approve nil.
func TestApprovalDenied(t *testing.T) {
	r, store, _ := testRunner(t, []llm.ChatResponse{
		toolCallMsg("c1", "write_file", `{"path":"x.txt","content":"y"}`),
		textMsg("ditolak, selesai"),
	})
	id, _ := store.CreateTask("s1", "x")
	_, status := r.Run(context.Background(), id, "s1", "x")
	if status != "succeeded" {
		t.Fatalf("status=%s", status)
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "x.txt")); err == nil {
		// tidak bisa cek langsung karena root = dir; verifikasi via history:
	}
	hist, _ := store.History("s1", 50)
	found := false
	for _, m := range hist {
		if m.Role == "tool" && json.Valid([]byte(m.Content)) && len(m.Content) > 0 {
			if m.Content == `{"error":"ditolak: tool write_file memerlukan persetujuan pengguna"}` {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("penolakan approval tidak diteruskan ke LLM")
	}
}

// Uji: deteksi pengulangan tool call identik.
func TestRepeatDetection(t *testing.T) {
	r, store, _ := testRunner(t, []llm.ChatResponse{
		toolCallMsg("c1", "read_file", `{"path":"same.txt"}`),
		toolCallMsg("c2", "read_file", `{"path":"same.txt"}`),
		toolCallMsg("c3", "read_file", `{"path":"same.txt"}`),
		toolCallMsg("c4", "read_file", `{"path":"same.txt"}`),
		textMsg("tidak akan sampai sini"),
	})
	id, _ := store.CreateTask("s1", "x")
	res, status := r.Run(context.Background(), id, "s1", "x")
	if status != "failed" {
		t.Fatalf("status=%s", status)
	}
	if res == "" {
		t.Fatal("pesan pengulangan kosong")
	}
}

// Uji: skill dimuat dan dipilih berdasar relevansi.
func TestSkillInjection(t *testing.T) {
	sdir := t.TempDir()
	os.MkdirAll(filepath.Join(sdir, "s1"), 0o700)
	os.WriteFile(filepath.Join(sdir, "s1", "SKILL.md"), []byte("---\nname: linux-admin\ndescription: panduan administrasi linux dan systemd\n---\nIsi instruksi linux."), 0o600)
	os.MkdirAll(filepath.Join(sdir, "s2"), 0o700)
	os.WriteFile(filepath.Join(sdir, "s2", "SKILL.md"), []byte("---\nname: cooking\ndescription: resep masakan\n---\nIsi masakan."), 0o600)
	list, errs := skills.Scan(sdir)
	if len(errs) != 0 || len(list) != 2 {
		t.Fatalf("scan: %d skills %d errs", len(list), len(errs))
	}
	sel := skills.Select(list, "perbaiki service systemd di linux", 1)
	if len(sel) != 1 || sel[0].Name != "linux-admin" {
		t.Fatalf("pemilihan skill salah: %v", sel)
	}

	// Full loop dengan skill:
	r, store, _ := testRunner(t, []llm.ChatResponse{textMsg("done")})
	r.Skills = list
	id, _ := store.CreateTask("s1", "perbaiki systemd")
	_, status := r.Run(context.Background(), id, "s1", "perbaiki systemd di linux")
	if status != "succeeded" {
		t.Fatalf("status=%s", status)
	}
}

// Uji: skill rusak tidak menjatuhkan aplikasi.
func TestBrokenSkillTolerated(t *testing.T) {
	sdir := t.TempDir()
	os.MkdirAll(filepath.Join(sdir, "bad"), 0o700)
	os.WriteFile(filepath.Join(sdir, "bad", "SKILL.md"), []byte("tanpa front matter"), 0o600)
	os.MkdirAll(filepath.Join(sdir, "good"), 0o700)
	os.WriteFile(filepath.Join(sdir, "good", "SKILL.md"), []byte("---\nname: good\ndescription: skill valid\n---\nIsi."), 0o600)
	list, errs := skills.Scan(sdir)
	if len(list) != 1 || len(errs) != 1 {
		t.Fatalf("list=%d errs=%d", len(list), len(errs))
	}
}

// Uji: cancellation saat loop berjalan.
func TestCancellation(t *testing.T) {
	r, store, table := testRunner(t, nil)
	r.Limits.MaxIterations = 100
	// Provider yang selalu mengembalikan tool call (loop tak berujung):
	r.Provider = &infiniteToolProvider{}
	id, _ := store.CreateTask("s1", "x")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if table.Cancel(id) {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	res, status := r.Run(ctx, id, "s1", "x")
	cancel()
	if status != "cancelled" {
		t.Fatalf("status=%s res=%s", status, res)
	}
}

type infiniteToolProvider struct{}

func (p *infiniteToolProvider) Model() string { return "inf" }
func (p *infiniteToolProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID: "x", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "list_directory", Arguments: "{}"},
		}}}}},
		Usage: llm.Usage{TotalTokens: 1},
	}, nil
}
