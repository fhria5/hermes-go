package config

import "testing"

func TestLoadInvalid(t *testing.T) {
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("konfigurasi tanpa API key harus ditolak")
	}
	t.Setenv("LLM_API_KEY", "x")
	if _, err := Load(); err == nil {
		t.Fatal("konfigurasi tanpa bot token harus ditolak")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	if _, err := Load(); err != nil {
		t.Fatalf("konfigurasi valid gagal: %v", err)
	}
}
