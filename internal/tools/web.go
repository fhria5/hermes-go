package tools

// Tahap C: web_search (DuckDuckGo HTML) dan web_fetch (reader-mode via r.jina.ai).
// Keduanya butuh jaringan; SSRF diwarisi dari client yang disuntikkan (ClientWeb).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ---------- WebSearch (DuckDuckGo HTML, tanpa API key) ----------

type WebSearchTool struct {
	Client  *http.Client
	MaxResp int64
}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "Cari di web (DuckDuckGo). Kembalikan judul + URL + cuplikan."
}
func (t *WebSearchTool) Permission() Permission { return PermApproval }

func (t *WebSearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string","description":"kata kunci pencarian"}}}`)
}

var ddgResultRe = regexp.MustCompile(`(?s)<a rel="nofollow" class="result__a" href="([^"]+)">(.*?)</a>`)
var ddgSnippetRe = regexp.MustCompile(`(?s)class="result__snippet"[^>]*>(.*?)</a>`)
var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string { return strings.TrimSpace(tagRe.ReplaceAllString(s, "")) }

func (t *WebSearchTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return Result{}, fmt.Errorf("query kosong")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://html.duckduckgo.com/html/?q="+url.QueryEscape(q), nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; HermesGo/0.2)")
	resp, err := t.Client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request gagal: %w", err)
	}
	defer resp.Body.Close()
	body, err := readLimited(resp, t.MaxResp)
	if err != nil {
		return Result{}, err
	}
	var sb strings.Builder
	titles := ddgResultRe.FindAllStringSubmatch(body, 8)
	snips := ddgSnippetRe.FindAllStringSubmatch(body, 8)
	if len(titles) == 0 {
		return Result{Output: "Tidak ada hasil (atau halaman berubah format)."}, nil
	}
	for i, m := range titles {
		link := m[1]
		// DDG paksa redirect via /l/?uddg=<encoded>
		if u, err := url.Parse(link); err == nil && u.Query().Get("uddg") != "" {
			link = u.Query().Get("uddg")
		}
		sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n", i+1, stripTags(m[2]), link))
		if i < len(snips) {
			sb.WriteString("   " + stripTags(snips[i][1]) + "\n")
		}
		sb.WriteString("\n")
	}
	return Result{Output: sb.String()}, nil
}

// ---------- WebFetch (reader mode r.jina.ai — hasil markdown bersih) ----------

type WebFetchTool struct {
	Client  *http.Client
	MaxResp int64
}

func (t *WebFetchTool) Name() string { return "web_fetch" }
func (t *WebFetchTool) Description() string {
	return "Ambil isi halaman web sebagai markdown bersih (reader mode)."
}
func (t *WebFetchTool) Permission() Permission { return PermApproval }

func (t *WebFetchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["url"],"properties":{"url":{"type":"string","format":"uri","description":"URL halaman yang dibaca"}}}`)
}

func (t *WebFetchTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	target, err := url.Parse(a.URL)
	if err != nil {
		return Result{}, fmt.Errorf("url tidak valid: %w", err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return Result{}, fmt.Errorf("protokol hanya http/https")
	}
	readerURL := "https://r.jina.ai/" + target.String()
	req, err := http.NewRequestWithContext(ctx, "GET", readerURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; HermesGo/0.2)")
	resp, err := t.Client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request gagal: %w", err)
	}
	defer resp.Body.Close()
	body, err := readLimited(resp, t.MaxResp)
	if err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("Sumber: %s\n\n%s", target.String(), body)}, nil
}

// readLimited: baca seluruh body dengan batas bytes (mencegah memori membengkak).
func readLimited(resp *http.Response, max int64) (string, error) {
	limited := io.LimitReader(resp.Body, max)
	b, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("baca body gagal: %w", err)
	}
	if int64(len(b)) >= max {
		b = append(b, []byte("\n...[truncated]")...)
	}
	return string(b), nil
}
