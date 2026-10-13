package main

import (
	"os"
	"testing"
)

// Uji parsing allowlist dalam kedua format (regresi bug v0.2.2).
func TestParseAllowedUsers(t *testing.T) {
	cases := map[string][]int64{
		"7804936884":       {7804936884},
		"1, 2,3":           {1, 2, 3},
		"[7804936884, 42]": {7804936884, 42},
		"":                 nil,
		"abc, 5":           {5},
	}
	for raw, want := range cases {
		os.Setenv("TELEGRAM_ALLOWED_USERS", raw)
		var got []int64
		if r := os.Getenv("TELEGRAM_ALLOWED_USERS"); r != "" {
			// replikasi logika main (diekstrak agar teruji)
			got = parseAllowedUsers(r)
		}
		if len(got) != len(want) {
			t.Fatalf("raw=%q: got %v want %v", raw, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("raw=%q: got %v want %v", raw, got, want)
			}
		}
	}
}
