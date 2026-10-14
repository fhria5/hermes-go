// Package telegram: antarmuka Telegram dengan allowlist user, antrean terbatas,
// rate limit per user, dedup update_id, dan command handling.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type API struct {
	token  string
	client *http.Client
	offset int64
	log    *slog.Logger
}

func NewAPI(token string, log *slog.Logger) *API {
	return &API{token: token, client: &http.Client{Timeout: 60 * time.Second}, log: log}
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Msg           `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Msg struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Text      string `json:"text"`
	Chat      *Chat  `json:"chat"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type apiResponse struct {
	OK          bool     `json:"ok"`
	Result      []Update `json:"result"`
	Description string   `json:"description"`
}

type apiResponseMsg struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Inline keyboard
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type CallbackQuery struct {
	ID   string `json:"id"`
	From *User  `json:"from"`
	Data string `json:"data"`
}

// Update diperluas dengan callback_query.
// (struct Update di atas tetap; tambahkan field CallbackQuery di sini via duplikasi
//  tidak perlu — sunting struct Update langsung di file ini pada baris struct utama.)

func (a *API) call(ctx context.Context, method string, params url.Values) ([]Update, error) {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/%s", a.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if method == "getUpdates" {
		var r apiResponse
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		if !r.OK {
			return nil, fmt.Errorf("telegram api error: %s", r.Description)
		}
		return r.Result, nil
	}
	var r apiResponseMsg
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("telegram api error %d: %s", resp.StatusCode, r.Description)
	}
	return nil, nil
}

// Update diperluas dengan callback_query (lihat struct Update).

// SendMessageWithButtons: kirim pesan dengan inline keyboard.
// Guard: buttons kosong atau marshal gagal → kirim tanpa reply_markup
// (Telegram menolak markup rusak dengan 400 "object expected as reply markup").
func (a *API) SendMessageWithButtons(ctx context.Context, chatID int64, text string, buttons []InlineButton) (int64, error) {
	vals := url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"text":    {truncateMsg(text)},
	}
	if len(buttons) > 0 {
		if kb, err := json.Marshal([][]InlineButton{buttons}); err == nil && len(kb) > 2 {
			vals["reply_markup"] = []string{string(kb)}
		}
	}
	_, err := a.call(ctx, "sendMessage", vals)
	return 0, err
}

// AnswerCallbackQuery: wajib dijawab agar spinner di client berhenti.
func (a *API) AnswerCallbackQuery(ctx context.Context, cbID, text string) {
	_, _ = a.call(ctx, "answerCallbackQuery", url.Values{
		"callback_query_id": {cbID},
		"text":              {truncateMsg(text)},
	})
}

// truncateMsg: batasi 4000 bytes DAN jangan memotong di tengah rune UTF-8
// (potongan rusak bisa membuat Telegram menolak pesan).
func truncateMsg(s string) string {
	if len(s) <= 4000 {
		return s
	}
	cut := s[:4000]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func (a *API) SendMessage(ctx context.Context, chatID int64, text string) error {
	// Telegram batas 4096 char; pecah.
	for len(text) > 4096 {
		if _, err := a.call(ctx, "sendMessage", url.Values{
			"chat_id": {strconv.FormatInt(chatID, 10)},
			"text":    {text[:4096]},
		}); err != nil {
			return err
		}
		text = text[4096:]
	}
	_, err := a.call(ctx, "sendMessage", url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"text":    {text},
	})
	return err
}

// GetUpdates long-polling dengan reconnect otomatis (di loop pemanggil).
func (a *API) GetUpdates(ctx context.Context, timeoutSec int) ([]Update, error) {
	res, err := a.call(ctx, "getUpdates", url.Values{
		"offset":          {strconv.FormatInt(a.offset, 10)},
		"timeout":         {strconv.Itoa(timeoutSec)},
		"allowed_updates": {`["message","callback_query"]`},
	})
	if err != nil {
		return nil, err
	}
	for _, u := range res {
		if u.UpdateID >= a.offset {
			a.offset = u.UpdateID + 1
		}
	}
	return res, nil
}

// Bot: pump update → handler dengan allowlist, dedup, antrean terbatas.
type Bot struct {
	API          *API
	AllowedUsers map[int64]bool
	Handler      func(ctx context.Context, userID, chatID int64, text string)
	CallbackFn   func(ctx context.Context, userID int64, data, cbID string)
	log          *slog.Logger

	seen      map[int64]bool
	seenMu    sync.Mutex
	queue     chan Update
	cbQueue   chan *CallbackQuery
	userTimes map[int64][]time.Time // rate limit burst 5/menit per user
	muteMu    sync.Mutex
}

func NewBot(api *API, allowed []int64, queueSize int, log *slog.Logger) *Bot {
	am := map[int64]bool{}
	for _, id := range allowed {
		am[id] = true
	}
	return &Bot{
		API: api, AllowedUsers: am, log: log,
		seen:      map[int64]bool{},
		queue:     make(chan Update, queueSize),
		cbQueue:   make(chan *CallbackQuery, queueSize),
		userTimes: map[int64][]time.Time{},
	}
}

// Run pump: getUpdates → dedup → allowlist → antrean → 1 worker (konkurensi 1
// agar RAM & SQLite aman; duplikat update_id dibuang).
func (b *Bot) Run(ctx context.Context) error {
	go b.worker(ctx)
	go b.cbWorker(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := b.API.GetUpdates(ctx, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.log.Error("getUpdates gagal, reconnect 5s", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if b.markSeen(u.UpdateID) {
				continue // duplikat — Telegram bisa mengirim ulang update_id sama
			}
			if u.CallbackQuery != nil {
				// Callback: validasi allowlist pemutus.
				if u.CallbackQuery.From == nil || !b.AllowedUsers[u.CallbackQuery.From.ID] {
					b.log.Warn("callback dari user tidak diizinkan", "user_id", cbUserID(u))
					b.API.AnswerCallbackQuery(ctx, u.CallbackQuery.ID, "Akses ditolak.")
					continue
				}
				select {
				case b.cbQueue <- u.CallbackQuery:
				case <-ctx.Done():
					return nil
				}
				continue
			}
			if u.Message == nil || u.Message.From == nil {
				continue
			}
			if !b.AllowedUsers[u.Message.From.ID] {
				b.log.Warn("user tidak diizinkan ditolak", "user_id", u.Message.From.ID)
				_ = b.API.SendMessage(ctx, chatID(u), "Akses ditolak.")
				continue
			}
			if b.muted(u.Message.From.ID) {
				_ = b.API.SendMessage(ctx, chatID(u), "Terlalu banyak pesan. Tunggu sebentar.")
				continue
			}
			select {
			case b.queue <- u:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func (b *Bot) cbWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cb := <-b.cbQueue:
			func() {
				defer func() {
					if r := recover(); r != nil {
						b.log.Error("panic callback", "err", r)
					}
				}()
				if b.CallbackFn != nil {
					b.CallbackFn(ctx, cb.From.ID, cb.Data, cb.ID)
				} else {
					b.API.AnswerCallbackQuery(ctx, cb.ID, "Handler tidak tersedia.")
				}
			}()
		}
	}
}

func cbUserID(u Update) int64 {
	if u.CallbackQuery != nil && u.CallbackQuery.From != nil {
		return u.CallbackQuery.From.ID
	}
	return 0
}

func (b *Bot) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-b.queue:
			func() {
				defer func() {
					if r := recover(); r != nil {
						b.log.Error("panic handler", "err", r)
					}
				}()
				b.Handler(ctx, u.Message.From.ID, chatID(u), u.Message.Text)
			}()
		}
	}
}

func chatID(u Update) int64 {
	if u.Message.Chat != nil {
		return u.Message.Chat.ID
	}
	return u.Message.From.ID
}

func (b *Bot) markSeen(id int64) bool {
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	if b.seen[id] {
		return true
	}
	b.seen[id] = true
	if len(b.seen) > 10_000 {
		b.seen = map[int64]bool{} // reset ringan; offset Telegram mencegah duplikasi massal
	}
	return false
}

// Rate limit per user: burst 5 pesan per menit (rolling).
// Sebelumnya 1 pesan/menit — terlalu ketat untuk pemakaian normal.
func (b *Bot) muted(uid int64) bool {
	b.muteMu.Lock()
	defer b.muteMu.Unlock()
	now := time.Now()
	// buang timestamps lebih lama dari 1 menit
	var keep []time.Time
	for _, t := range b.userTimes[uid] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 5 {
		b.userTimes[uid] = keep
		return true
	}
	b.userTimes[uid] = append(keep, now)
	return false
}
