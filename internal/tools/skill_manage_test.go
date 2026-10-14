package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSkillTool(t *testing.T) (*SkillManageTool, string) {
	t.Helper()
	dir := t.TempDir()
	return &SkillManageTool{SkillsDir: dir, MaxSkillChars: 10000}, dir
}

func TestSkillManageCreateAndScan(t *testing.T) {
	tr, dir := newTestSkillTool(t)
	res, err := tr.Run(context.Background(), json.RawMessage(
		`{"action":"create","name":"catatan-kerja","description":"Catatan rutin harian.","content":"1. Selalu verifikasi dulu."}`))
	if err != nil {
		t.Fatalf("create gagal: %v", err)
	}
	if !strings.Contains(res.Output, "dibuat") {
		t.Fatalf("output tak terduga: %s", res.Output)
	}
	b, err := os.ReadFile(filepath.Join(dir, "catatan-kerja", "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "name: catatan-kerja") {
		t.Fatalf("file skill tidak sesuai: %v", err)
	}
}

func TestSkillManagePatch(t *testing.T) {
	tr, _ := newTestSkillTool(t)
	tr.Run(context.Background(), json.RawMessage(
		`{"action":"create","name":"rutin","description":"Rutin.","content":"1. Langkah satu."}`))
	res, err := tr.Run(context.Background(), json.RawMessage(
		`{"action":"patch","name":"rutin","old_text":"1. Langkah satu.","content":"1. Langkah satu DUA."}`))
	if err != nil || !strings.Contains(res.Output, "diperbaiki") {
		t.Fatalf("patch gagal: %v %s", err, res.Output)
	}
	b, _ := os.ReadFile(filepath.Join(tr.SkillsDir, "rutin", "SKILL.md"))
	if !strings.Contains(string(b), "DUA") {
		t.Fatalf("patch tidak diterapkan")
	}
}

func TestSkillManageDelete(t *testing.T) {
	tr, dir := newTestSkillTool(t)
	tr.Run(context.Background(), json.RawMessage(
		`{"action":"create","name":"hapus-aku","description":"x.","content":"isi"}`))
	if _, err := tr.Run(context.Background(), json.RawMessage(`{"action":"delete","name":"hapus-aku"}`)); err != nil {
		t.Fatalf("delete gagal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hapus-aku")); !os.IsNotExist(err) {
		t.Fatalf("skill masih ada")
	}
}

func TestSkillManageTraversalDitolak(t *testing.T) {
	tr, _ := newTestSkillTool(t)
	for _, name := range []string{"../evil", "a/b", "UPPER", ".hidden", ""} {
		if _, err := tr.Run(context.Background(), json.RawMessage(
			`{"action":"create","name":"`+name+`","description":"x","content":"y"}`)); err == nil {
			t.Errorf("nama %q seharusnya ditolak", name)
		}
	}
}

func TestSkillManagePatchUnknownOldText(t *testing.T) {
	tr, _ := newTestSkillTool(t)
	tr.Run(context.Background(), json.RawMessage(
		`{"action":"create","name":"ada","description":"x.","content":"teks asli"}`))
	if _, err := tr.Run(context.Background(), json.RawMessage(
		`{"action":"patch","name":"ada","old_text":"TIDAK ADA","content":"z"}`)); err == nil {
		t.Fatal("patch dengan old_text hantu seharusnya gagal")
	}
}
