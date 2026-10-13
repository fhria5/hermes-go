// Package tools: canonicalisasi argumen untuk pengikatan approval.
//
// Normalisasi (didokumentasikan):
//   - Argumen di-parse sebagai JSON; jika bukan JSON object → error.
//   - Key object diurutkan leksikografis (byte-wise) secara rekursif.
//   - Angka dinormalisasi lewat encoding/json Number (presisi dipertahankan apa
//     adanya; 1 dan 1.0 tetap berbeda karena JSON mempertahankan bentuknya —
//     ini disengaja: argumen yang dipersetujui harus identik byte demi byte
//     setelah kanonikalisasi).
//   - Whitespace dihapus (compact). String di-escape ulang secara standar.
//   - Array tidak diurutkan (urutan array bermakna).
//
// Dengan aturan di atas, {"b":2,"a":1} dan {"a":1,"b":2} menghasilkan kanonik
// yang sama, sehingga approval tidak bisa dibatalkan hanya dengan mengubah
// urutan key.
package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// CanonicalJSON mengembalikan representasi JSON deterministik dari objek args.
func CanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("argumen bukan JSON valid: %w", err)
	}
	if _, ok := v.(map[string]interface{}); !ok {
		return nil, fmt.Errorf("argumen harus JSON object")
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return json.RawMessage(buf.Bytes()), nil
}

func writeCanonical(buf *bytes.Buffer, v interface{}) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		b, _ := json.Marshal(t)
		buf.Write(b)
	case json.Number:
		buf.WriteString(t.String())
	case []interface{}:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("tipe JSON tidak didukung: %T", v)
	}
	return nil
}

// ActionIdentity: identitas tindakan yang immutable untuk sebuah approval.
type ActionIdentity struct {
	TaskID        int64
	Tool          string
	CanonicalArgs json.RawMessage
}

// BindHash menghitung hash kriptografis atas identitas tindakan lengkap
// (task ID + nama tool + argumen kanonik). Domain-separated dengan panjang
// prefix agar "ab"+"c" tidak pernah bertabrakan dengan "a"+"bc".
//
// Hash ini BUKAN autentikasi: ia hanya pengikat integritas. Otorisasi pengguna,
// status approval, dan validasi task tetap wajib diperiksa terpisah.
func BindHash(taskID int64, tool string, canonicalArgs json.RawMessage) string {
	h := sha256.New()
	writeField := func(b []byte) {
		h.Write([]byte(strconv.Itoa(len(b))))
		h.Write([]byte{':'})
		h.Write(b)
	}
	writeField([]byte(strconv.FormatInt(taskID, 10)))
	writeField([]byte(tool))
	writeField(canonicalArgs)
	return hex.EncodeToString(h.Sum(nil))
}

// HashArgs lama dipertahankan sebagai helper (hash kanonik + identitas).
func HashArgs(args json.RawMessage) string {
	c, err := CanonicalJSON(args)
	if err != nil {
		c = args
	}
	return BindHash(0, "", c)
}
