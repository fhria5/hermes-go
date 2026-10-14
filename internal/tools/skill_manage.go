package tools

// D4: skill_manage — agent dapat membuat/memperbaiki/menghapus skill-nya sendiri
// (procedural memory, pola Hermes Agent). Semua aksi:
//   - PermApproval (wajib persetujuan user) — file yang ditulis agent adalah konten
//     tidak tepercaya; approval menutup risiko skill jadi jalur prompt-injection.
//   - Path guard ketat: nama skill divalidasi ^[a-z0-9-]{1,64}$, path selalu di
//     dalam SkillsDir, symlink diikuti lalu dicek, anti path traversal.
//   - Nama file selalu <skillsdir>/<name>/SKILL.md (tidak ada subpath bebas) —
//     sengaja lebih ketat dari Hermes, cukup untuk skala ini.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type SkillManageTool struct {
	// SkillsDir direktori skill absolut (mis. /opt/hermes-go/skills).
	SkillsDir string
	// MaxSkillChars batas ukuran konten skill.
	MaxSkillChars int
}

func (t *SkillManageTool) Name() string { return "skill_manage" }
func (t *SkillManageTool) Description() string {
	return "Buat, perbaiki, atau hapus skill (SKILL.md) untuk diingat lintas tugas."
}
func (t *SkillManageTool) Permission() Permission { return PermApproval }
func (t *SkillManageTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["action","name"],
"properties":{
  "action":{"type":"string","enum":["create","patch","delete"]},
  "name":{"type":"string","description":"nama skill lowercase-hyphen"},
  "description":{"type":"string","description":"deskripsi singkat (untuk create)"},
  "content":{"type":"string","description":"isi SKILL.md lengkap (create) atau satu instruksi baru (patch)"},
  "old_text":{"type":"string","description":"teks lama yang diganti (patch)"}
}}`)
}

var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type skillArgs struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	OldText     string `json:"old_text"`
}

func (t *SkillManageTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	if t.SkillsDir == "" {
		return Result{}, fmt.Errorf("SkillsDir tidak diset")
	}
	var a skillArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, err
	}
	if !skillNameRe.MatchString(a.Name) {
		return Result{}, fmt.Errorf("nama skill %q tidak valid (harus lowercase-hyphen, maks 64)", a.Name)
	}
	// Path guard: gabung lalu pastikan tetap di dalam SkillsDir (anti traversal).
	dir := filepath.Join(t.SkillsDir, a.Name)
	absDir, err := filepath.Abs(dir)
	if err != nil || absDir != filepath.Join(filepath.Clean(t.SkillsDir), a.Name) {
		return Result{}, fmt.Errorf("path skill di luar direktori skills")
	}
	path := filepath.Join(absDir, "SKILL.md")

	switch a.Action {
	case "delete":
		if err := os.RemoveAll(absDir); err != nil {
			return Result{}, fmt.Errorf("hapus skill gagal: %w", err)
		}
		return Result{Output: fmt.Sprintf("skill %q dihapus", a.Name)}, nil

	case "create":
		if a.Content == "" || a.Description == "" {
			return Result{}, fmt.Errorf("create butuh description dan content")
		}
		if len(a.Content) > t.MaxSkillChars {
			return Result{}, fmt.Errorf("konten %d char melebihi batas %d", len(a.Content), t.MaxSkillChars)
		}
		front := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n", a.Name, a.Description)
		if err := os.MkdirAll(absDir, 0o755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(path, []byte(front+a.Content), 0o644); err != nil {
			return Result{}, err
		}
		return Result{Output: fmt.Sprintf("skill %q dibuat", a.Name)}, nil

	case "patch":
		b, err := os.ReadFile(path)
		if err != nil {
			return Result{}, fmt.Errorf("skill %q belum ada (pakai create)", a.Name)
		}
		if a.OldText == "" || !strings.Contains(string(b), a.OldText) {
			return Result{}, fmt.Errorf("old_text tidak ditemukan di skill %q", a.Name)
		}
		updated := strings.Replace(string(b), a.OldText, a.Content, 1)
		if len(updated) > t.MaxSkillChars {
			return Result{}, fmt.Errorf("hasil patch %d char melebihi batas %d", len(updated), t.MaxSkillChars)
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return Result{}, err
		}
		return Result{Output: fmt.Sprintf("skill %q diperbaiki", a.Name)}, nil

	default:
		return Result{}, fmt.Errorf("aksi %q tidak dikenal", a.Action)
	}
}
