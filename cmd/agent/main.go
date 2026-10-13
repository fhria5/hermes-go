// Hermes-Go: AI agent modular dengan Telegram, agent loop, tools, skills, SQLite.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"hermes-go/internal/agent"
	"hermes-go/internal/config"
	"hermes-go/internal/llm"
	"hermes-go/internal/observability"
	"hermes-go/internal/scheduler"
	"hermes-go/internal/skills"
	"hermes-go/internal/storage"
	"hermes-go/internal/telegram"
	"hermes-go/internal/tools"
)

// approvalChats: task_id → chat_id pemohon (untuk mengirim tombol approval
// dan memvalidasi sesi pemutus). Mutex-protected; hanya di memori —
// setelah restart approval pending sudah expired di DB (fail-closed).
type approvalChats struct {
	mu sync.Mutex
	m  map[int64]int64
}

func newApprovalChats() *approvalChats { return &approvalChats{m: map[int64]int64{}} }
func (a *approvalChats) set(taskID, chatID int64) {
	a.mu.Lock()
	a.m[taskID] = chatID
	a.mu.Unlock()
}
func (a *approvalChats) get(taskID int64) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m[taskID]
}

func sessionChatID(sessionID string) int64 {
	// format "tg:<id>"
	var id int64
	fmt.Sscanf(sessionID, "tg:%d", &id)
	return id
}

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
	// Approval pending dari proses mati → expired (fail-closed; tidak dieksekusi ulang).
	if err := store.ExpireApprovals(time.Now().UTC().Format(time.RFC3339)); err != nil {
		log.Warn("expire approvals gagal", "err", err)
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

	bridge := agent.NewApprovalBridge(16)
	runner := &agent.Runner{
		Provider: provider, Registry: reg, Store: store, TaskTable: taskTable,
		Limits: agent.Limits{
			MaxIterations: cfg.Agent.MaxIterations, MaxToolCalls: cfg.Agent.MaxToolCalls,
			MaxTokens: cfg.Agent.MaxTokensPerTask, ToolTimeout: cfg.Agent.ToolTimeout,
			MaxOutput: int(cfg.Agent.MaxOutputBytes),
		},
		Bridge:      bridge,
		ApprovalTTL: 5 * time.Minute,
		Skills:      skillList, Log: log,
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
	chats := newApprovalChats()

	// ---- Approval pump: request dari agent loop → pesan tombol Telegram ----
	go bridge.Consume(ctx, func(req *agent.ApprovalRequest) {
		chatID := chats.get(req.TaskID)
		if chatID == 0 {
			return // tidak tahu tujuan → TTL menolak (fail-closed)
		}
		ap := &storage.Approval{
			ID: req.ID, TaskID: req.TaskID, SessionID: fmt.Sprintf("tg:%d", chatID),
			Tool: req.Tool, ArgsHash: HashArgs(req.Args), ArgsPreview: tools.PreviewArgs(req.Args, 300),
			RequestedAt: time.Now().UTC().Format(time.RFC3339),
			ExpiresAt:   time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339),
		}
		if err := store.CreateApproval(ap); err != nil {
			log.Error("simpan approval gagal", "err", err)
			return
		}
		text := fmt.Sprintf("⚠️ Persetujuan diperlukan (tugas #%d)\nTool: %s\nArgumen:\n%s\n\nKedaluwarsa 5 menit.", req.TaskID, req.Tool, ap.ArgsPreview)
		btns := []telegram.InlineButton{
			{Text: "✅ Setujui", CallbackData: "apr:ok:" + req.ID},
			{Text: "❌ Tolak", CallbackData: "apr:no:" + req.ID},
		}
		if _, err := api.SendMessageWithButtons(ctx, chatID, text, btns); err != nil {
			log.Error("kirim approval gagal", "err", err)
		}
	})

	// ---- Callback handler: validasi identitas, status, kedaluwarsa, atomik ----
	bot.CallbackFn = func(ctx context.Context, userID int64, data, cbID string) {
		switch {
		case strings.HasPrefix(data, "apr:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) != 3 || (parts[1] != "ok" && parts[1] != "no") {
				api.AnswerCallbackQuery(ctx, cbID, "Callback tidak valid.")
				return
			}
			apprID := parts[2]
			now := time.Now().UTC().Format(time.RFC3339)
			ap, err := store.GetApproval(apprID, now) // menolak bukan-pending & kedaluwarsa
			if err != nil {
				api.AnswerCallbackQuery(ctx, cbID, "Tidak dapat diproses (kedaluwarsa/sudah diproses).")
				return
			}
			if ap.SessionID != fmt.Sprintf("tg:%d", userID) {
				log.Warn("approval ditolak: user bukan pemohon sesi", "user", userID)
				api.AnswerCallbackQuery(ctx, cbID, "Bukan sesi Anda.")
				return
			}
			approve := parts[1] == "ok"
			// Atomik: hanya satu callback yang menang (UPDATE WHERE status='pending' AND expires_at>now).
			applied, err := store.DecideApproval(apprID, approve, userID, now)
			if err != nil || !applied {
				api.AnswerCallbackQuery(ctx, cbID, "Sudah diproses.")
				return
			}
			if approve {
				api.AnswerCallbackQuery(ctx, cbID, "Disetujui.")
			} else {
				api.AnswerCallbackQuery(ctx, cbID, "Ditolak.")
			}
			bridge.Resolve(apprID, approve, userID)
		case strings.HasPrefix(data, "clr:"):
			if data == "clr:ok" {
				if err := store.ClearSession(fmt.Sprintf("tg:%d", userID)); err != nil {
					api.AnswerCallbackQuery(ctx, cbID, "Gagal menghapus.")
					return
				}
				api.AnswerCallbackQuery(ctx, cbID, "Sesi dihapus.")
			} else {
				api.AnswerCallbackQuery(ctx, cbID, "Batal.")
			}
		default:
			api.AnswerCallbackQuery(ctx, cbID, "Tidak dikenal.")
		}
	}

	// ---- Scheduler ----
	sched := scheduler.New(store, func(prompt, sessionID string) {
		chatID := sessionChatID(sessionID)
		taskID, err := store.CreateTask(sessionID, prompt)
		if err != nil {
			log.Error("scheduler: buat task gagal", "err", err)
			return
		}
		chats.set(taskID, chatID)
		_ = api.SendMessage(ctx, chatID, fmt.Sprintf("⏰ Tugas terjadwal #%d dimulai.", taskID))
		go func() {
			result, status := runner.Run(ctx, taskID, sessionID, prompt)
			log.Info("tugas terjadwal selesai", "task_id", taskID, "status", status)
			_ = api.SendMessage(ctx, chatID, fmt.Sprintf("Tugas terjadwal #%d [%s]\n%s", taskID, status, result))
		}()
	})
	go sched.Run(ctx)

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
		case strings.HasPrefix(text, "/sched"):
			handleSched(ctx, api, store, sessionID, chatID, text)
		case text == "/clear":
			if _, err := api.SendMessageWithButtons(ctx, chatID,
				"Hapus seluruh riwayat pesan sesi ini? Memory jangka panjang TIDAK terhapus.",
				[]telegram.InlineButton{
					{Text: "✅ Ya, hapus sesi", CallbackData: "clr:ok"},
					{Text: "❌ Batal", CallbackData: "clr:no"},
				}); err != nil {
				log.Error("kirim konfirmasi clear gagal", "err", err)
			}
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
			chats.set(taskID, chatID)
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

// handleSched: perintah /sched Telegram.
func handleSched(ctx context.Context, api *telegram.API, store *storage.Store, sessionID string, chatID int64, text string) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		_ = api.SendMessage(ctx, chatID, "Format: /sched list|add|on|off|del")
		return
	}
	switch fields[1] {
	case "list":
		list, _ := store.ListSchedules(sessionID)
		out := "Jadwal:\n"
		for _, s := range list {
			en := "off"
			if s.Enabled {
				en = "on"
			}
			out += fmt.Sprintf("#%d [%s] %s %s next=%s\n", s.ID, en, s.Name, s.Kind, s.NextRunAt)
		}
		_ = api.SendMessage(ctx, chatID, out)
	case "add":
		// /sched add <nama> interval <detik> <prompt...> | once <RFC3339> <prompt...>
		if len(fields) < 5 {
			_ = api.SendMessage(ctx, chatID, "Format: /sched add <nama> interval <detik> <prompt> | once <RFC3339> <prompt>")
			return
		}
		sc := &storage.Schedule{SessionID: sessionID, Name: fields[2], Kind: fields[3], TZ: "Asia/Jakarta", Enabled: true}
		switch sc.Kind {
		case "interval":
			sec, err := strconv.Atoi(fields[4])
			if err != nil || sec < 60 || sec > 30*24*3600 {
				_ = api.SendMessage(ctx, chatID, "Interval harus 60..2592000 detik.")
				return
			}
			sc.IntervalSec = sec
			sc.Prompt = strings.Join(fields[5:], " ")
			sc.NextRunAt = time.Now().UTC().Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
		case "once":
			t, err := time.Parse(time.RFC3339, fields[4])
			if err != nil || t.Before(time.Now()) {
				_ = api.SendMessage(ctx, chatID, "Waktu harus RFC3339 di masa depan.")
				return
			}
			sc.RunAt = t.UTC().Format(time.RFC3339)
			sc.Prompt = strings.Join(fields[5:], " ")
			sc.NextRunAt = sc.RunAt
		default:
			_ = api.SendMessage(ctx, chatID, "kind: interval|once")
			return
		}
		if sc.Prompt == "" {
			_ = api.SendMessage(ctx, chatID, "Prompt wajib.")
			return
		}
		id, err := store.CreateSchedule(sc)
		if err != nil {
			_ = api.SendMessage(ctx, chatID, "Gagal: "+err.Error())
			return
		}
		_ = api.SendMessage(ctx, chatID, fmt.Sprintf("Jadwal #%d dibuat.", id))
	case "on", "off":
		if len(fields) < 3 {
			_ = api.SendMessage(ctx, chatID, "Format: /sched on|off <id>")
			return
		}
		var id int64
		if _, err := fmt.Sscanf(fields[2], "%d", &id); err != nil {
			_ = api.SendMessage(ctx, chatID, "Format: /sched on|off <id>")
			return
		}
		ok, err := store.SetScheduleEnabled(id, sessionID, fields[1] == "on")
		if err != nil || !ok {
			_ = api.SendMessage(ctx, chatID, "Jadwal tidak ditemukan.")
			return
		}
		_ = api.SendMessage(ctx, chatID, "OK.")
	case "del":
		if len(fields) < 3 {
			_ = api.SendMessage(ctx, chatID, "Format: /sched del <id>")
			return
		}
		var id int64
		if _, err := fmt.Sscanf(fields[2], "%d", &id); err != nil {
			_ = api.SendMessage(ctx, chatID, "Format: /sched del <id>")
			return
		}
		ok, _ := store.DeleteSchedule(id, sessionID)
		if ok {
			_ = api.SendMessage(ctx, chatID, "Dihapus.")
		} else {
			_ = api.SendMessage(ctx, chatID, "Tidak ditemukan.")
		}
	}
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

// HashArgs: hash argumen untuk pengikatan approval ke argumen persis.
func HashArgs(args json.RawMessage) string {
	h := sha256.Sum256(args)
	return hex.EncodeToString(h[:])
}
