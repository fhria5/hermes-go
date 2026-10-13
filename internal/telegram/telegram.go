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
	UpdateID int64 `json:"update_id"`
	Message  *Msg  `json:"message"`
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
		"allowed_updates": {`["message"]`},
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
	log          *slog.Logger

	seen     map[int64]bool
	seenMu   sync.Mutex
	queue    chan Update
	userMute map[int64]time.Time // rate limit sederhana per user
	muteMu   sync.Mutex
}

func NewBot(api *API, allowed []int64, queueSize int, log *slog.Logger) *Bot {
	am := map[int64]bool{}
	for _, id := range allowed {
		am[id] = true
	}
	return &Bot{
		API: api, AllowedUsers: am, log: log,
		seen:     map[int64]bool{},
		queue:    make(chan Update, queueSize),
		userMute: map[int64]time.Time{},
	}
}

// Run pump: getUpdates → dedup → allowlist → antrean → 1 worker (konkurensi 1
// agar RAM & SQLite aman; duplikat update_id dibuang).
func (b *Bot) Run(ctx context.Context) error {
	go b.worker(ctx)
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

func (b *Bot) muted(uid int64) bool {
	b.muteMu.Lock()
	defer b.muteMu.Unlock()
	if t, ok := b.userMute[uid]; ok && time.Since(t) < time.Minute {
		return true
	}
	b.userMute[uid] = time.Now()
	return false
}
