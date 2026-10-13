package tools

import (
	"encoding/json"
	"testing"
)

// Uji: key order berbeda → canonical sama.
func TestCanonicalKeyOrder(t *testing.T) {
	a, _ := CanonicalJSON(json.RawMessage(`{"b":2,"a":1}`))
	b, _ := CanonicalJSON(json.RawMessage(`{"a":1,"b":2}`))
	if string(a) != string(b) {
		t.Fatalf("canonical beda: %s vs %s", a, b)
	}
	if string(a) != `{"a":1,"b":2}` {
		t.Fatalf("canonical salah: %s", a)
	}
}

// Uji: whitespace dihapus.
func TestCanonicalWhitespace(t *testing.T) {
	a, _ := CanonicalJSON(json.RawMessage(`{ "path" :  "a.txt"  }`))
	b, _ := CanonicalJSON(json.RawMessage(`{"path":"a.txt"}`))
	if string(a) != string(b) {
		t.Fatalf("whitespace mempengaruhi canonical: %s", a)
	}
}

// Uji: nested object & array (array tidak diurutkan).
func TestCanonicalNested(t *testing.T) {
	a, _ := CanonicalJSON(json.RawMessage(`{"x":[1,{"z":true,"y":null}],"w":"s"}`))
	want := `{"w":"s","x":[1,{"y":null,"z":true}]}`
	if string(a) != want {
		t.Fatalf("got=%s want=%s", a, want)
	}
}

// Uji: bukan object / JSON rusak → error.
func TestCanonicalInvalid(t *testing.T) {
	if _, err := CanonicalJSON(json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("array harus ditolak")
	}
	if _, err := CanonicalJSON(json.RawMessage(`nope`)); err == nil {
		t.Fatal("json rusak harus ditolak")
	}
}

// Uji: BindHash domain-separated — perubahan task/tool/args mengubah hash.
func TestBindHashSensitivity(t *testing.T) {
	c1, _ := CanonicalJSON(json.RawMessage(`{"path":"a.txt"}`))
	c2, _ := CanonicalJSON(json.RawMessage(`{"path":"b.txt"}`))
	h1 := BindHash(1, "write_file", c1)
	h1b := BindHash(1, "write_file", c1)
	h2 := BindHash(1, "write_file", c2)
	h3 := BindHash(2, "write_file", c1)
	h4 := BindHash(1, "shell", c1)
	if h1 != h1b {
		t.Fatal("hash tidak deterministik")
	}
	if h1 == h2 || h1 == h3 || h1 == h4 {
		t.Fatal("hash tidak peka perubahan identitas tindakan")
	}
	// Domain separation: field digabung tidak boleh ambigu.
	ha := BindHash(12, "ab", c1)
	hb := BindHash(1, "2ab", c1)
	if ha == hb {
		t.Fatal("domain separation lemah (prefix length tidak bekerja)")
	}
}
