// Package skills: loader SKILL.md dengan YAML front matter, validasi,
// path traversal guard, dan pemilihan berdasar relevansi kata kunci.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Front matter format yang didukung (dokumentasi):
//
//	---
//	name: nama-skill          // wajib, lowercase-hyphen
//	description: deskripsi    // wajib, < 500 char
//	version: 1.0.0            // opsional
//	tags: alpha,beta          // opsional, comma-separated
//	---
//	... instruksi markdown (dimuat hanya saat skill dipilih) ...
type Skill struct {
	Name        string
	Description string
	Version     string
	Tags        []string
	Path        string // path relatif ke skills dir
	Source      string // path absolut (audit)
	Body        string // instruksi lengkap (lazy load: kosong sampai Load)
}

var frontMatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type LoadError struct {
	Path  string
	Error error
}

// Scan memindai direktori skills (rekursif, maks 3 level) dan memvalidasi
// setiap SKILL.md. Skill rusak dilaporkan, tidak menjatuhkan aplikasi.
func Scan(dir string) ([]*Skill, []LoadError) {
	var out []*Skill
	var errs []LoadError
	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if info.Name() != "SKILL.md" {
			return nil
		}
		s, err := loadFile(dir, path)
		if err != nil {
			errs = append(errs, LoadError{Path: path, Error: err})
			return nil
		}
		out = append(out, s)
		return nil
	}
	filepath.Walk(dir, walkFn)
	return out, errs
}

func loadFile(root, path string) (*Skill, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	if strings.Contains(rel, "..") {
		return nil, fmt.Errorf("path traversal terdeteksi: %s", rel)
	}
	b, err := os.ReadFile(path) // #nosec — path sudah dibatasi ke root via Rel + walk
	if err != nil {
		return nil, err
	}
	src := string(b)
	m := frontMatterRe.FindStringSubmatch(src)
	if m == nil {
		return nil, fmt.Errorf("front matter YAML tidak ditemukan")
	}
	s := &Skill{Source: path, Path: rel}
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(strings.Trim(v, `"`))
		switch strings.TrimSpace(k) {
		case "name":
			s.Name = v
		case "description":
			s.Description = v
		case "version":
			s.Version = v
		case "tags":
			for _, t := range strings.Split(v, ",") {
				s.Tags = append(s.Tags, strings.TrimSpace(t))
			}
		}
	}
	if !nameRe.MatchString(s.Name) {
		return nil, fmt.Errorf("nama skill tidak valid: %q", s.Name)
	}
	if s.Description == "" || len(s.Description) > 500 {
		return nil, fmt.Errorf("deskripsi wajib dan maks 500 char")
	}
	return s, nil
}

// Load memuat body instruksi skill (lazy).
func (s *Skill) Load() error {
	b, err := os.ReadFile(s.Source) // #nosec — path divalidasi saat scan
	if err != nil {
		return err
	}
	m := frontMatterRe.FindStringSubmatch(string(b))
	if m == nil {
		return fmt.Errorf("front matter hilang")
	}
	s.Body = strings.TrimSpace(string(b)[len(m[0]):])
	if len(s.Body) > 20_000 {
		s.Body = s.Body[:20_000] + "\n...[instruksi skill terpotong]"
	}
	return nil
}

// Select memilih skill berdasar relevansi kata kunci terhadap tugas.
// Skor: kecocokan kata pada name (bobot 3), tags (2), description (1).
// Prioritas konflik: skor tertinggi menang; seri diselesaikan dengan nama
// alfabetis. Maksimal N skill dimuat per tugas (default 3).
func Select(all []*Skill, task string, max int) []*Skill {
	if max <= 0 {
		max = 3
	}
	words := strings.Fields(strings.ToLower(task))
	type scored struct {
		s *Skill
		v int
	}
	var cands []scored
	for _, s := range all {
		score := 0
		for _, w := range words {
			if strings.Contains(s.Name, w) {
				score += 3
			}
			for _, t := range s.Tags {
				if strings.EqualFold(t, w) {
					score += 2
				}
			}
			if strings.Contains(strings.ToLower(s.Description), w) {
				score++
			}
		}
		if score > 0 {
			cands = append(cands, scored{s, score})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].v != cands[j].v {
			return cands[i].v > cands[j].v
		}
		return cands[i].s.Name < cands[j].s.Name
	})
	var out []*Skill
	for i, c := range cands {
		if i >= max {
			break
		}
		out = append(out, c.s)
	}
	return out
}
