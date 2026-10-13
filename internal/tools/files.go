package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Filesystem tools dengan pembatasan ke workspace root, penanganan symlink aman,
// batas ukuran file, dan pemisahan hak baca/tulis.

type FSConfig struct {
	Root         string
	MaxFileBytes int64
	WriteEnabled bool
}

func (c FSConfig) resolve(path string) (string, error) {
	if path == "" {
		path = "."
	}
	// Join + Clean, lalu pastikan hasil tetap di dalam root setelah EvalSymlinks
	// pada komponen yang sudah ada (path traversal & symlink escape).
	abs := filepath.Clean(filepath.Join(c.Root, path))
	if !strings.HasPrefix(abs, filepath.Clean(c.Root)+string(filepath.Separator)) && abs != filepath.Clean(c.Root) {
		return "", fmt.Errorf("path di luar workspace: %s", path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		if !strings.HasPrefix(resolved, filepath.Clean(c.Root)+string(filepath.Separator)) && resolved != filepath.Clean(c.Root) {
			return "", fmt.Errorf("symlink mengarah di luar workspace: %s", path)
		}
	}
	return abs, nil
}

type readResult struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

type ReadFileTool struct{ Cfg FSConfig }

func (t *ReadFileTool) Name() string           { return "read_file" }
func (t *ReadFileTool) Description() string    { return "Baca isi file di dalam workspace." }
func (t *ReadFileTool) Permission() Permission { return PermAuto }
func (t *ReadFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string","description":"path relatif ke workspace"}}}`)
}

func (t *ReadFileTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	abs, err := t.Cfg.resolve(a.Path)
	if err != nil {
		return Result{}, err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return Result{}, fmt.Errorf("stat: %w", err)
	}
	if st.IsDir() {
		return Result{}, fmt.Errorf("%s adalah direktori", a.Path)
	}
	if st.Size() > t.Cfg.MaxFileBytes {
		return Result{}, fmt.Errorf("file %d byte melebihi batas %d", st.Size(), t.Cfg.MaxFileBytes)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(readResult{Path: a.Path, Size: st.Size(), Content: string(b)})
	return Result{Output: string(out)}, nil
}

type writeResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type WriteFileTool struct{ Cfg FSConfig }

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "Tulis file (atomik) di dalam workspace. Menimpa seluruh isi."
}
func (t *WriteFileTool) Permission() Permission { return PermApproval }
func (t *WriteFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["path","content"],"properties":{"path":{"type":"string"},"content":{"type":"string"}}}`)
}

func (t *WriteFileTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	if !t.Cfg.WriteEnabled {
		return Result{}, fmt.Errorf("penulisan dinonaktifkan")
	}
	abs, err := t.Cfg.resolve(a.Path)
	if err != nil {
		return Result{}, err
	}
	if int64(len(a.Content)) > t.Cfg.MaxFileBytes {
		return Result{}, fmt.Errorf("konten %d byte melebihi batas %d", len(a.Content), t.Cfg.MaxFileBytes)
	}
	// Tulis atomik: pastikan direktori induk ada, temp file di direktori yang sama, lalu rename.
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return Result{}, err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, []byte(a.Content), 0o600); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return Result{}, err
	}
	out, _ := json.Marshal(writeResult{Path: a.Path, Bytes: len(a.Content)})
	return Result{Output: string(out)}, nil
}

type ListDirectoryTool struct{ Cfg FSConfig }

func (t *ListDirectoryTool) Name() string           { return "list_directory" }
func (t *ListDirectoryTool) Description() string    { return "Daftar isi direktori di dalam workspace." }
func (t *ListDirectoryTool) Permission() Permission { return PermAuto }
func (t *ListDirectoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}

func (t *ListDirectoryTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &a)
	abs, err := t.Cfg.resolve(a.Path)
	if err != nil {
		return Result{}, err
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return Result{}, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		names = append(names, e.Name()+suffix)
	}
	sort.Strings(names)
	out, _ := json.Marshal(names)
	return Result{Output: string(out)}, nil
}

type SearchFilesTool struct{ Cfg FSConfig }

func (t *SearchFilesTool) Name() string { return "search_files" }
func (t *SearchFilesTool) Description() string {
	return "Cari substring di file teks workspace (rekursif, maks 100 hasil)."
}
func (t *SearchFilesTool) Permission() Permission { return PermAuto }
func (t *SearchFilesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`)
}

func (t *SearchFilesTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	var hits []string
	err := filepath.WalkDir(t.Cfg.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(hits) >= 100 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > t.Cfg.MaxFileBytes {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if strings.Contains(string(b), a.Query) {
			rel, _ := filepath.Rel(t.Cfg.Root, p)
			hits = append(hits, rel)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(hits)
	return Result{Output: string(out)}, nil
}
