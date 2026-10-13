package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mockServer(handler http.HandlerFunc) (*OpenAICompat, *httptest.Server) {
	ts := httptest.NewServer(handler)
	return &OpenAICompat{BaseURL: ts.URL, APIKey: "test", ModelName: "m", HTTPClient: ts.Client(), MaxRetries: 2}, ts
}

func chatJSON(content string) string {
	b, _ := json.Marshal(ChatResponse{Choices: []Choice{{Message: Message{Role: "assistant", Content: content}}}})
	return string(b)
}

// Uji: respons sukses.
func TestProviderSuccess(t *testing.T) {
	p, ts := mockServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatJSON("halo")))
	})
	defer ts.Close()
	resp, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	if err != nil || resp.Choices[0].Message.Content != "halo" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

// Uji: HTTP 429 + Retry-After → dihormati lalu sukses pada attempt kedua.
func TestProviderRateLimitRetryAfter(t *testing.T) {
	attempt := 0
	p, ts := mockServer(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(chatJSON("ok")))
	})
	defer ts.Close()
	start := time.Now()
	resp, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	if err != nil || resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("Retry-After tidak dihormati")
	}
}

// Uji: HTTP 5xx → transient, di-retry.
func TestProvider5xx(t *testing.T) {
	attempt := 0
	p, ts := mockServer(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(chatJSON("recover")))
	})
	defer ts.Close()
	resp, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	if err != nil || resp.Choices[0].Message.Content != "recover" {
		t.Fatalf("err=%v", err)
	}
}

// Uji: HTTP 4xx permanen → TIDAK di-retry.
func TestProvider4xxNoRetry(t *testing.T) {
	attempt := 0
	p, ts := mockServer(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	})
	defer ts.Close()
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("harus error")
	}
	if attempt != 1 {
		t.Fatalf("4xx di-retry %d kali — tidak boleh", attempt)
	}
}

// Uji: respons tidak valid (bukan JSON) → error decode.
func TestProviderBadJSON(t *testing.T) {
	p, ts := mockServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("bukan json"))
	})
	defer ts.Close()
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("bad json harus error")
	}
}

// Uji: context cancellation saat menunggu.
func TestProviderContextCancel(t *testing.T) {
	p, ts := mockServer(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Write([]byte(chatJSON("late")))
	})
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := p.Chat(ctx, ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("harus dibatalkan")
	}
}

// Uji: timeout HTTP.
func TestProviderTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer ts.Close()
	p := &OpenAICompat{BaseURL: ts.URL, APIKey: "k", ModelName: "m",
		HTTPClient: &http.Client{Timeout: 100 * time.Millisecond}, MaxRetries: 0}
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("timeout harus error")
	}
}

// Uji: tool call response ter-dekode dengan benar.
func TestProviderToolCallDecode(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]}}],"usage":{"total_tokens":5}}`
	p, ts := mockServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	defer ts.Close()
	resp, err := p.Chat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	tcs := resp.Choices[0].Message.ToolCalls
	if len(tcs) != 1 || tcs[0].Function.Name != "read_file" {
		t.Fatalf("tool call salah: %+v", tcs)
	}
}
