// Package agent: jembatan approval antara agent loop dan layer Telegram/DB.
// Fail-closed: kedaluwarsa, error, atau channel penuh → tolak.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// ApprovalRequest dipancarkan agent loop ketika tool PermApproval dipanggil.
type ApprovalRequest struct {
	ID          string
	TaskID      int64
	Tool        string
	Args        json.RawMessage
	ArgsPreview string // diisi Telegram layer dengan sanitasi sebelum ditampilkan
	Done        chan Decision
}

type Decision struct {
	Approved bool
	By       int64
}

// ApprovalBridge: agent loop → request channel → Telegram mengirim tombol →
// callback memutus → decision kembali ke loop.
type ApprovalBridge struct {
	Requests chan *ApprovalRequest

	mu      sync.Mutex
	pending map[string]*ApprovalRequest
}

func NewApprovalBridge(queue int) *ApprovalBridge {
	return &ApprovalBridge{Requests: make(chan *ApprovalRequest, queue), pending: map[string]*ApprovalRequest{}}
}

// Consume: pump goroutine — ambil request dari channel, daftarkan ke pending
// agar Resolve bisa menemukannya, lalu serahkan ke handler (kirim pesan tombol).
func (b *ApprovalBridge) Consume(ctx context.Context, handle func(*ApprovalRequest)) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-b.Requests:
			b.mu.Lock()
			b.pending[req.ID] = req
			b.mu.Unlock()
			handle(req)
		}
	}
}

// Resolve dipanggil handler callback Telegram. Mengembalikan false jika ID
// tidak dikenal atau sudah diproses (decision hanya dikirim sekali).
func (b *ApprovalBridge) Resolve(id string, approved bool, by int64) bool {
	b.mu.Lock()
	req, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	req.Done <- Decision{Approved: approved, By: by}
	return true
}

// WaitForApproval dipanggil agent loop (blocking dengan TTL; fail-closed).
// Mengembalikan (approved, decidedBy, approvalID).
func (b *ApprovalBridge) WaitForApproval(ctx context.Context, taskID int64, tool string, args json.RawMessage, ttl time.Duration) (bool, int64, string) {
	id := newID()
	req := &ApprovalRequest{
		ID: id, TaskID: taskID, Tool: tool, Args: args,
		Done: make(chan Decision, 1),
	}
	select {
	case b.Requests <- req:
	case <-ctx.Done():
		return false, 0, id
	}
	deadline := time.NewTimer(ttl)
	defer deadline.Stop()
	select {
	case d := <-req.Done:
		return d.Approved, d.By, id
	case <-deadline.C:
		b.Resolve(id, false, 0) // bersihkan pending
		return false, 0, id
	case <-ctx.Done():
		b.Resolve(id, false, 0)
		return false, 0, id
	}
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// argsHash: pengikat approval ke argumen persis (perubahan argumen = persetujuan baru).
func argsHash(args json.RawMessage) string {
	h := sha256.Sum256(args)
	return hex.EncodeToString(h[:])
}
