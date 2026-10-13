// Package tools: registry tool dengan schema, validasi, timeout, batas output, izin.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"hermes-go/internal/llm"
)

// Permission menyatakan level izin eksekusi sebuah tool.
type Permission int

const (
	PermAuto     Permission = iota // aman, boleh jalan tanpa persetujuan
	PermApproval                   // wajib persetujuan user sebelum jalan
	PermDisabled                   // dinonaktifkan oleh policy
)

type Result struct {
	Output string
	Err    string
}

type Tool interface {
	Name() string
	Description() string
	// Parameters: JSON Schema object.
	Parameters() json.RawMessage
	Permission() Permission
	Run(ctx context.Context, args json.RawMessage) (Result, error)
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

func (r *Registry) Register(t Tool) error {
	n := t.Name()
	if n == "" || strings.ContainsAny(n, " \t\n") {
		return fmt.Errorf("nama tool tidak valid: %q", n)
	}
	if _, dup := r.tools[n]; dup {
		return fmt.Errorf("tool duplikat: %s", n)
	}
	r.tools[n] = t
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) { t, ok := r.tools[name]; return t, ok }

// AllowedTools mengembalikan definisi LLM untuk semua tool yang tidak PermDisabled.
func (r *Registry) AllowedTools() []llm.ToolDef {
	defs := []llm.ToolDef{}
	for _, t := range r.tools {
		if t.Permission() == PermDisabled {
			continue
		}
		def := llm.ToolDef{Type: "function"}
		def.Function.Name = t.Name()
		def.Function.Description = t.Description()
		def.Function.Parameters = t.Parameters()
		defs = append(defs, def)
	}
	return defs
}

// Validate: args harus JSON object dan sesuai "required" di schema (validasi minimal tapi nyata).
func Validate(schema json.RawMessage, args json.RawMessage) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var a map[string]interface{}
	if err := json.Unmarshal(args, &a); err != nil {
		return fmt.Errorf("argumen bukan JSON object: %w", err)
	}
	var s struct {
		Required []string `json:"required"`
	}
	if len(schema) > 0 {
		if err := json.Unmarshal(schema, &s); err != nil {
			return fmt.Errorf("schema tool tidak valid: %w", err)
		}
	}
	for _, req := range s.Required {
		if _, ok := a[req]; !ok {
			return fmt.Errorf("argumen wajib hilang: %s", req)
		}
	}
	return nil
}

// RunWithPolicy: validasi args, cek izin, jalankan dengan timeout & batas output.
// approved=true artinya persetujuan user sudah diberikan (dari layer approval).
func (r *Registry) RunWithPolicy(ctx context.Context, name string, args json.RawMessage, approved bool, timeout time.Duration, maxOutput int) (Result, error) {
	t, ok := r.Get(name)
	if !ok {
		return Result{}, fmt.Errorf("tool tidak dikenal: %s", name)
	}
	if t.Permission() == PermDisabled {
		return Result{}, fmt.Errorf("tool %s dinonaktifkan oleh policy", name)
	}
	if t.Permission() == PermApproval && !approved {
		return Result{}, ErrApprovalRequired
	}
	if err := Validate(t.Parameters(), args); err != nil {
		return Result{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := t.Run(cctx, args)
	if err != nil {
		return Result{}, err
	}
	if maxOutput > 0 && len(res.Output) > maxOutput {
		res.Output = res.Output[:maxOutput] + "\n...[output terpotong]"
	}
	return res, nil
}

var ErrApprovalRequired = fmt.Errorf("approval required")
