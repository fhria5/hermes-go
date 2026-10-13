// Package llm menyediakan provider OpenAI-compatible Chat Completions
// dengan tool calling, retry backoff+jitter, dan penghormatan Retry-After.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

type Message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"` // string atau nil saat tool_calls ada
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

type ToolDef struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"` // JSON Schema
	} `json:"function"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
}

type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// RateLimitError menandakan error yang layak diulang dengan backoff.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "rate limited" }

type TransientError struct{ Err error }

func (e *TransientError) Error() string { return "transient: " + e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	Model() string
}

type OpenAICompat struct {
	BaseURL    string
	APIKey     string
	ModelName  string
	HTTPClient *http.Client
	MaxRetries int
}

func (p *OpenAICompat) Model() string { return p.ModelName }

func (p *OpenAICompat) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt <= p.MaxRetries; attempt++ {
		if attempt > 0 {
			d := backoffDur(attempt, lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		resp, err := p.doOnce(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var rl *RateLimitError
		var tr *TransientError
		if !(errors.As(err, &rl) || errors.As(err, &tr)) {
			return nil, err // permanent, jangan retry
		}
	}
	return nil, fmt.Errorf("gagal setelah %d attempt: %w", p.MaxRetries+1, lastErr)
}

func backoffDur(attempt int, lastErr error) time.Duration {
	if rl, ok := lastErr.(*RateLimitError); ok && rl.RetryAfter > 0 {
		return rl.RetryAfter
	}
	base := time.Second * time.Duration(math.Pow(2, float64(attempt-1)))
	jitter := time.Duration(rand.Int63n(int64(base / 2)))
	return base + jitter
}

func (p *OpenAICompat) doOnce(ctx context.Context, body []byte) (*ChatResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("buat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &TransientError{Err: err}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, &TransientError{Err: err}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		ra := resp.Header.Get("Retry-After")
		d := 5 * time.Second
		if sec, err := strconv.Atoi(ra); err == nil {
			d = time.Duration(sec) * time.Second
		}
		return nil, &RateLimitError{RetryAfter: d}
	case resp.StatusCode >= 500:
		return nil, &TransientError{Err: fmt.Errorf("server %d: %s", resp.StatusCode, truncate(data, 500))}
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, truncate(data, 500))
	}
	var out ChatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("response tanpa choices")
	}
	return &out, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...[truncated]"
}

// MockProvider untuk testing tanpa kredit API.
type MockProvider struct {
	Responses []ChatResponse
	Calls     int
	ModelName string
}

func (m *MockProvider) Model() string { return m.ModelName }

func (m *MockProvider) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	if m.Calls >= len(m.Responses) {
		return nil, fmt.Errorf("mock: kehabisan response preset")
	}
	r := m.Responses[m.Calls]
	m.Calls++
	return &r, nil
}
