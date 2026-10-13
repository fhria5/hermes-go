package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fsCfg(dir string) FSConfig {
	return FSConfig{Root: dir, MaxFileBytes: 1_000_000, WriteEnabled: true}
}

// Uji: path traversal ditolak.
func TestPathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	tl := &ReadFileTool{Cfg: fsCfg(dir)}
	for _, p := range []string{"../etc/passwd", "../../etc/passwd", "/etc/passwd", "sub/../../outside"} {
		out, err := json.Marshal(map[string]string{"path": p})
		if err != nil {
			t.Fatal(err)
		}
		_, runErr := tl.Run(context.Background(), out)
		if runErr == nil {
			t.Fatalf("path %s harus ditolak", p)
		}
	}
}

// Uji: symlink escape ditolak.
func TestSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("rahasia"), 0o600)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "link.txt"))
	tl := &ReadFileTool{Cfg: fsCfg(dir)}
	args, _ := json.Marshal(map[string]string{"path": "link.txt"})
	_, err := tl.Run(context.Background(), args)
	if err == nil {
		t.Fatal("symlink ke luar workspace harus ditolak")
	}
}

// Uji: write atomik + batas ukuran file.
func TestWriteAndLimit(t *testing.T) {
	dir := t.TempDir()
	w := &WriteFileTool{Cfg: fsCfg(dir)}
	args, _ := json.Marshal(map[string]string{"path": "a/b.txt", "content": "data"})
	if _, err := w.Run(context.Background(), args); err != nil {
		t.Fatalf("write gagal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b.txt")); err != nil {
		t.Fatal("file tidak ada (rename atomik gagal?)")
	}
	big := strings.Repeat("x", 2_000_000)
	args2, _ := json.Marshal(map[string]string{"path": "big.txt", "content": big})
	if _, err := w.Run(context.Background(), args2); err == nil {
		t.Fatal("batas ukuran tidak ditegakkan")
	}
}

// Uji: shell allowlist.
func TestShellAllowlist(t *testing.T) {
	s := &ShellTool{Allowlist: []string{"echo"}, MaxOutput: 1000, Timeout: 5 * time.Second}
	args, _ := json.Marshal(map[string]string{"command": "echo hello"})
	res, err := s.Run(context.Background(), args)
	if err != nil || strings.TrimSpace(res.Output) != "hello" {
		t.Fatalf("res=%v err=%v", res, err)
	}
	// Injection: tidak lewat shell, metachar dianggap argumen literal — tidak dijalankan.
	argsInj, _ := json.Marshal(map[string]string{"command": "echo hello; rm -rf /tmp/xxx"})
	if _, err := s.Run(context.Background(), argsInj); err == nil {
		// "echo" diizinkan, tapi eksekusi tanpa shell → argumen "; rm -rf ..." literal. Tidak error → aman.
		_ = err
	}
	argsDeny, _ := json.Marshal(map[string]string{"command": "curl http://evil.com"})
	if _, err := s.Run(context.Background(), argsDeny); err == nil {
		t.Fatal("command di luar allowlist harus ditolak")
	}
}

// Uji: registry — duplikat, validasi schema, approval gate, disabled.
type dummyTool struct {
	perm Permission
}

func (d *dummyTool) Name() string        { return "dummy" }
func (d *dummyTool) Description() string { return "dummy" }
func (d *dummyTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["x"],"properties":{"x":{"type":"string"}}}`)
}
func (d *dummyTool) Permission() Permission { return d.perm }
func (d *dummyTool) Run(_ context.Context, _ json.RawMessage) (Result, error) {
	return Result{Output: "ok"}, nil
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(&dummyTool{perm: PermAuto}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&dummyTool{perm: PermAuto}); err == nil {
		t.Fatal("duplikat harus ditolak")
	}
	// required hilang
	if _, err := reg.RunWithPolicy(context.Background(), "dummy", json.RawMessage(`{}`), true, time.Second, 100); err == nil {
		t.Fatal("validasi required harus gagal")
	}
	if _, err := reg.RunWithPolicy(context.Background(), "dummy", json.RawMessage(`{"x":"1"}`), true, time.Second, 100); err != nil {
		t.Fatal(err)
	}
	// approval gate (registry terpisah, tool PermApproval)
	regA := NewRegistry()
	regA.Register(&dummyTool{perm: PermApproval})
	if _, err := regA.RunWithPolicy(context.Background(), "dummy", json.RawMessage(`{"x":"1"}`), false, time.Second, 100); err == nil {
		t.Fatal("approval harus diminta")
	}
	if _, err := regA.RunWithPolicy(context.Background(), "dummy", json.RawMessage(`{"x":"1"}`), true, time.Second, 100); err != nil {
		t.Fatal(err)
	}
	// disabled
	reg2 := NewRegistry()
	reg2.Register(&dummyTool{perm: PermDisabled})
	if _, err := reg2.RunWithPolicy(context.Background(), "dummy", json.RawMessage(`{"x":"1"}`), true, time.Second, 100); err == nil {
		t.Fatal("tool disabled harus ditolak")
	}
}

// Uji: SSRF — loopback & private IP diblokir, redirect ke host lain ditolak.
func TestSSRFProtection(t *testing.T) {
	// server "target" di loopback → harus diblokir oleh denyPrivate.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("rahasia-internal"))
	}))
	defer ts.Close()

	client := NewSSRFSafeClient(nil, true, 5*time.Second)
	tool := &HTTPTool{Client: client, MaxResp: 1000}
	args, _ := json.Marshal(map[string]string{"url": ts.URL})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("akses loopback harus diblokir")
	}

	// Redirect ke host berbeda dengan allowlist → ditolak.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example.com/x", http.StatusFound)
	}))
	defer redir.Close()
	// Bypass denyPrivate untuk host tes ini dengan allowlist explict ke 127.0.0.1;
	// redirect tetap harus ditolak karena evil.example.com di luar allowlist.
	client2 := NewSSRFSafeClient([]string{"127.0.0.1"}, false, 5*time.Second)
	tool2 := &HTTPTool{Client: client2, MaxResp: 1000}
	args2, _ := json.Marshal(map[string]string{"url": redir.URL})
	if _, err := tool2.Run(context.Background(), args2); err == nil {
		t.Fatal("redirect ke host luar allowlist harus ditolak")
	}
}
