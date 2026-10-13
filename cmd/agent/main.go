// Hermes-Go: AI agent modular dengan Telegram, agent loop, tools, skills, SQLite.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"hermes-go/internal/agent"
	"hermes-go/internal/config"
	"hermes-go/internal/llm"
	"hermes-go/internal/observability"
	"hermes-go/internal/skills"
	"hermes-go/internal/storage"
	"hermes-go/internal/telegram"
	"hermes-go/internal/tools"
)

func main() {
	log := observability.NewLogger()
	cfg, err := config.Load()
	if err != nil {
		log.Error("konfigurasi tidak valid", "err", err)
		os.Exit(1)
	}

	store, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		log.Error("buka database gagal", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// Rekonsiliasi: tugas running/waiting_approval saat restart → status tidak
	// diketahui (proses mati di tengah). Tandai failed dengan catatan; JANGAN
	// ulangi otomatis efek samping.
	if pending, err := store.RunningTasks(); err == nil {
		for _, t := range pending {
			_ = store.SetTaskStatus(t.ID, "failed")
			log.Warn("tugas tertinggal saat restart ditandai failed", "task_id", t.ID)
		}
	}

	reg := tools.NewRegistry()
	fsCfg := tools.FSConfig{Root: cfg.Agent.WorkspaceRoot, MaxFileBytes: cfg.Agent.MaxFileBytes, WriteEnabled: true}
	_ = os.MkdirAll(fsCfg.Root, 0o700)
	reg.Register(&tools.ReadFileTool{Cfg: fsCfg})
	reg.Register(&tools.ListDirectoryTool{Cfg: fsCfg})
	reg.Register(&tools.SearchFilesTool{Cfg: fsCfg})
	reg.Register(&tools.WriteFileTool{Cfg: fsCfg})
	reg.Register(&tools.ShellTool{Allowlist: cfg.Agent.ShellAllowlist, MaxOutput: int(cfg.Agent.MaxOutputBytes), Timeout: cfg.Agent.ToolTimeout})
	reg.Register(&tools.HTTPTool{Client: tools.NewSSRFSafeClient(cfg.Security.AllowedHosts, cfg.Security.DenyPrivateNet, cfg.Security.HTTPTimeout), MaxResp: cfg.Security.MaxHTTPRespBytes})
	memAdapter := &storeAdapter{store: store}
	reg.Register(&tools.SearchMemoryTool{Store: memAdapter})
	reg.Register(&tools.SaveMemoryTool{Store: memAdapter})
	taskTable := tools.NewTaskTable()
	reg.Register(&tools.TaskStatusTool{Store: memAdapter, Tasks: taskTable})
	reg.Register(&tools.TaskCancelTool{Tasks: taskTable})

	provider := &llm.OpenAICompat{
		BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, ModelName: cfg.LLM.Model,
		HTTPClient: &http.Client{Timeout: cfg.LLM.Timeout}, MaxRetries: cfg.LLM.MaxRetries,
	}
	skillList, skillErrs := skills.Scan(cfg.Agent.SkillsDir)
	for _, se := range skillErrs {
		log.Warn("skill rusak dilewati", "path", se.Path, "err", se.Error)
	}
	log.Info("skill dimuat", "count", len(skillList))

	runner := &agent.Runner{
		Provider: provider, Registry: reg, Store: store, TaskTable: taskTable,
		Limits: agent.Limits{
			MaxIterations: cfg.Agent.MaxIterations, MaxToolCalls: cfg.Agent.MaxToolCalls,
			MaxTokens: cfg.Agent.MaxTokensPerTask, ToolTimeout: cfg.Agent.ToolTimeout,
			MaxOutput: int(cfg.Agent.MaxOutputBytes),
		},
		Approve: nil, // mode aman default: tool berisiko ditolak sampai approval diimplementasi via chat
		Skills:  skillList, Log: log,
	}

	// Health check lokal saja (loopback), nonaktifkan akses publik.
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			if err := store.DB.Ping(); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, "db down")
				return
			}
			fmt.Fprint(w, "ok")
		})
		_ = http.ListenAndServe("127.0.0.1:"+port(cfg.HealthAddr), mux)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	api := telegram.NewAPI(cfg.Telegram.BotToken, log)
	var allowed []int64
	_ = json.Unmarshal([]byte(os.Getenv("TELEGRAM_ALLOWED_USERS")), &allowed)
	bot := telegram.NewBot(api, allowed, 10, log)
	bot.Handler = func(ctx context.Context, userID, chatID int64, text string) {
		sessionID := fmt.Sprintf("tg:%d", userID)
		switch {
		case text == "/start", text == "/help":
			msg := "Hermes-Go agent. Kirim tugas sebagai pesan biasa.\n" +
				"/status — status tugas terakhir\n/tasks — daftar tugas\n" +
				"/cancel <id> — batalkan tugas\n/memory <query> — cari memory\n/clear — (belum diimplementasi)"
			_ = api.SendMessage(ctx, chatID, msg)
		case text == "/tasks":
			tasks, _ := store.ListTasks(sessionID, 10)
			out := "Tugas terakhir:\n"
			for _, t := range tasks {
				out += fmt.Sprintf("#%d [%s] %s\n", t.ID, t.Status, truncateS(t.Description, 60))
			}
			_ = api.SendMessage(ctx, chatID, out)
		case strings.HasPrefix(text, "/cancel"):
			var id int64
			if _, err := fmt.Sscanf(text, "/cancel %d", &id); err != nil {
				_ = api.SendMessage(ctx, chatID, "Format: /cancel <id>")
				return
			}
			if taskTable.Cancel(id) {
				_ = api.SendMessage(ctx, chatID, fmt.Sprintf("Tugas %d dibatalkan.", id))
			} else {
				_ = api.SendMessage(ctx, chatID, fmt.Sprintf("Tugas %d tidak sedang berjalan.", id))
			}
		case strings.HasPrefix(text, "/memory"):
			q := strings.TrimSpace(strings.TrimPrefix(text, "/memory"))
			hits, _ := store.SearchMemory(q, 10)
			out := "Memory:\n"
			for _, h := range hits {
				out += fmt.Sprintf("[%s] %s\n", h.Key, truncateS(h.Content, 100))
			}
			_ = api.SendMessage(ctx, chatID, out)
		case strings.HasPrefix(text, "/status"):
			tasks, _ := store.ListTasks(sessionID, 1)
			if len(tasks) == 0 {
				_ = api.SendMessage(ctx, chatID, "Belum ada tugas.")
				return
			}
			_ = api.SendMessage(ctx, chatID, fmt.Sprintf("#%d [%s]", tasks[0].ID, tasks[0].Status))
		case text == "/clear":
			_ = api.SendMessage(ctx, chatID, "/clear belum diimplementasi.")
		default:
			if strings.HasPrefix(text, "/") {
				_ = api.SendMessage(ctx, chatID, "Perintah tidak dikenal. /help")
				return
			}
			taskID, err := store.CreateTask(sessionID, text)
			if err != nil {
				_ = api.SendMessage(ctx, chatID, "Gagal membuat tugas.")
				return
			}
			_ = api.SendMessage(ctx, chatID, fmt.Sprintf("Tugas #%d diterima, sedang dikerjakan.", taskID))
			start := time.Now()
			result, status := runner.Run(ctx, taskID, sessionID, text)
			log.Info("tugas selesai", "task_id", taskID, "status", status, "durasi", time.Since(start).String())
			_ = api.SendMessage(ctx, chatID, fmt.Sprintf("Tugas #%d [%s]\n\n%s", taskID, status, result))
		}
	}

	log.Info("Hermes-Go berjalan", "model", provider.Model(), "skills", len(skillList))
	if err := bot.Run(ctx); err != nil {
		log.Error("bot berhenti", "err", err)
	}
	log.Info("shutdown tertib")
}

func port(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return "8089"
}

func truncateS(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type storeAdapter struct{ store *storage.Store }

func (a *storeAdapter) SaveMemory(k, c string) error { return a.store.SaveMemory(k, c) }
func (a *storeAdapter) SearchMemory(q string, l int) ([]tools.MemHit, error) {
	hits, err := a.store.SearchMemory(q, l)
	if err != nil {
		return nil, err
	}
	out := make([]tools.MemHit, len(hits))
	for i, h := range hits {
		out[i] = tools.MemHit{Key: h.Key, Content: h.Content}
	}
	return out, nil
}
func (a *storeAdapter) DeleteMemory(k string) (int64, error) { return a.store.DeleteMemory(k) }
func (a *storeAdapter) GetTask(id int64) (*tools.TaskRow, error) {
	t, err := a.store.GetTask(id)
	if err != nil {
		return nil, err
	}
	return &tools.TaskRow{ID: t.ID, Status: t.Status, Description: t.Description}, nil
}
func (a *storeAdapter) SetTaskStatus(id int64, s string) error { return a.store.SetTaskStatus(id, s) }
