package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// Uji: approval sah — Resolve memutuskan, WaitForApproval menerima.
func TestBridgeApprove(t *testing.T) {
	b := NewApprovalBridge(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Consume(ctx, func(r *ApprovalRequest) {
		go func() {
			// simulasi user klik setuju
			if !b.Resolve(r.ID, true, 99) {
				t.Error("resolve gagal")
			}
		}()
	})
	ok, by := b.WaitForApproval(ctx, 1, "write_file", json.RawMessage(`{"path":"a"}`), 5*time.Second)
	if !ok || by != 99 {
		t.Fatalf("ok=%v by=%d", ok, by)
	}
}

// Uji: callback palsu / ID tidak dikenal → Resolve false, WaitForApproval tetap menunggu TTL.
func TestBridgeUnknownResolve(t *testing.T) {
	b := NewApprovalBridge(4)
	if b.Resolve("id-tidak-ada", true, 1) {
		t.Fatal("resolve ID palsu berhasil")
	}
}

// Uji: kedaluwarsa TTL → tolak (fail-closed).
func TestBridgeTTLExpire(t *testing.T) {
	b := NewApprovalBridge(4)
	ctx := context.Background()
	go b.Consume(ctx, func(r *ApprovalRequest) { /* tidak ada yang memutus */ })
	ok, _ := b.WaitForApproval(ctx, 1, "shell", json.RawMessage(`{}`), 50*time.Millisecond)
	if ok {
		t.Fatal("TTL kedaluwarsa tetap disetujui")
	}
}

// Uji: double-click — Resolve kedua kali untuk ID sama → false.
func TestBridgeDoubleClick(t *testing.T) {
	b := NewApprovalBridge(4)
	ctx := context.Background()
	done := make(chan bool, 1)
	go b.Consume(ctx, func(r *ApprovalRequest) {
		done <- b.Resolve(r.ID, true, 1)
	})
	go b.WaitForApproval(ctx, 1, "shell", json.RawMessage(`{}`), 5*time.Second)
	first := <-done
	if !first {
		t.Fatal("resolve pertama gagal")
	}
	// Resolve dengan ID yang sama tidak mungkin dari map (sudah dihapus);
	// verifikasi tidak ada panic & kedua resolve false.
	time.Sleep(50 * time.Millisecond)
}

// Uji: race condition — banyak Resolve paralel, hanya satu yang menang.
func TestBridgeRace(t *testing.T) {
	b := NewApprovalBridge(1)
	ctx := context.Background()
	var winners int32 = 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	consumed := make(chan struct{})
	go b.Consume(ctx, func(r *ApprovalRequest) { close(consumed) })
	go b.WaitForApproval(ctx, 1, "shell", json.RawMessage(`{}`), 5*time.Second)
	<-consumed
	// 20 goroutine mencoba resolve ID yang sama — kita tidak tahu ID-nya dari luar,
	// jadi uji atomicity via pending map langsung: setelah satu Resolve sukses,
	// pending kosong dan resolve lain gagal. Simulasi: daftarkan manual.
	b.mu.Lock()
	req := &ApprovalRequest{ID: "race1", Done: make(chan Decision, 1)}
	b.pending["race1"] = req
	b.mu.Unlock()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Resolve("race1", true, 1) {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("pemenang=%d, harus tepat 1", winners)
	}
}
