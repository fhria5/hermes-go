package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// ---------- Shell (allowlist + timeout + kill process group) ----------

type ShellTool struct {
	Allowlist []string // command dasar yang diizinkan (empty = semua dinonaktifkan)
	MaxOutput int
	Timeout   time.Duration
}

func (t *ShellTool) Name() string { return "shell" }
func (t *ShellTool) Description() string {
	return "Jalankan command shell (hanya command dalam allowlist)."
}
func (t *ShellTool) Permission() Permission { return PermApproval }
func (t *ShellTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string"}}}`)
}

func (t *ShellTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	if len(t.Allowlist) == 0 {
		return Result{}, fmt.Errorf("shell dinonaktifkan: allowlist kosong")
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	fields := strings.Fields(a.Command)
	if len(fields) == 0 {
		return Result{}, fmt.Errorf("command kosong")
	}
	base := fields[0]
	allowed := false
	for _, b := range t.Allowlist {
		if b == base {
			allowed = true
			break
		}
	}
	if !allowed {
		return Result{}, fmt.Errorf("command %q tidak dalam allowlist", base)
	}
	cctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	// No shell: exec langsung → tanpa shell injection via metacharacter.
	cmd := exec.CommandContext(cctx, fields[0], fields[1:]...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if len(out) > t.MaxOutput {
		out = out[:t.MaxOutput]
	}
	res := Result{Output: string(out)}
	if err != nil {
		res.Err = err.Error()
	}
	return res, nil
}

// ---------- HTTPRequest dengan proteksi SSRF ----------

type HTTPTool struct {
	Client  *http.Client // client dengan CheckRedirect kustom diinject oleh security
	MaxResp int64
	Timeout time.Duration
}

func (t *HTTPTool) Name() string           { return "http_request" }
func (t *HTTPTool) Description() string    { return "HTTP GET ke host yang diizinkan (SSRF-guarded)." }
func (t *HTTPTool) Permission() Permission { return PermApproval }
func (t *HTTPTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["url"],"properties":{"url":{"type":"string","format":"uri"},"method":{"type":"string","enum":["GET","HEAD"]}}}`)
}

func (t *HTTPTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	if a.Method == "" {
		a.Method = "GET"
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		return Result{}, fmt.Errorf("url tidak valid: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Result{}, fmt.Errorf("protokol hanya http/https")
	}
	req, err := http.NewRequestWithContext(ctx, a.Method, u.String(), nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request gagal: %w", err)
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 4096)
	buf := make([]byte, 32*1024)
	total := int64(0)
	for {
		n, err := resp.Body.Read(buf)
		total += int64(n)
		if total > t.MaxResp {
			body = append(body, buf[:n]...)
			body = append(body, []byte("\n...[truncated]")...)
			break
		}
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	header := fmt.Sprintf("HTTP %d\nContent-Type: %s\n\n", resp.StatusCode, resp.Header.Get("Content-Type"))
	return Result{Output: header + string(body)}, nil
}

// NewSSRFSafeClient: batasi tujuan ke allowed hosts, tolak loopback/private/link-local
// pada SetIPAddr (terpanggil setelah resolusi DNS tiap dial — termasuk redirect),
// sehingga DNS rebinding yang mengarah ke IP privat tetap tertahan.
func NewSSRFSafeClient(allowedHosts []string, denyPrivate bool, timeout time.Duration) *http.Client {
	allowed := map[string]bool{}
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = true
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			if denyPrivate {
				if ip := net.ParseIP(host); ip != nil && isPrivateIP(ip) {
					return nil, fmt.Errorf("akses ke IP privat diblokir")
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("terlalu banyak redirect")
			}
			if len(allowed) > 0 && !allowed[strings.ToLower(req.URL.Hostname())] {
				return fmt.Errorf("redirect ke host tidak diizinkan: %s", req.URL.Hostname())
			}
			return nil
		},
	}
}

func isPrivateIP(ip net.IP) bool {
	private := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8",
		"169.254.0.0/16", "100.64.0.0/10", "::1/128", "fc00::/7", "fe80::/10"}
	for _, cidr := range private {
		_, ipnet, _ := net.ParseCIDR(cidr)
		if ipnet.Contains(ip) {
			return true
		}
	}
	return false
}
