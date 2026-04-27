package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/provider"
)

// recordedSSE is a paste of a real OpenAI streaming response with two text
// chunks and a final usage chunk. Whitespace + blank-line framing match
// OpenAI's actual output.
const recordedSSE = `data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":", world"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}

data: [DONE]

`

func TestStream_HappyPath(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want Bearer sk-test", got)
		}
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := p.Stream(ctx, provider.Request{
		Model:    "openai/gpt-4o-mini",
		System:   "You are helpful.",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "say hi"}},
		Options:  provider.Options{MaxOutputTokens: 50},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text strings.Builder
	var usage *provider.Usage
	for d := range ch {
		switch d.Kind {
		case provider.DeltaText:
			text.WriteString(d.Text)
		case provider.DeltaUsage:
			usage = d.Usage
		case provider.DeltaError:
			t.Fatalf("unexpected error delta: %v", d.Err)
		}
	}
	if got := text.String(); got != "Hello, world" {
		t.Errorf("assembled text = %q, want %q", got, "Hello, world")
	}
	if usage == nil || usage.InputTokens != 12 || usage.OutputTokens != 3 {
		t.Errorf("usage = %+v, want {Input:12 Output:3}", usage)
	}

	// Verify the request body includes stream:true, include_usage, system
	// prompt, and the user message.
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["model"] != "gpt-4o-mini" {
		t.Errorf("body.model = %v, want gpt-4o-mini", body["model"])
	}
	if body["stream"] != true {
		t.Errorf("body.stream = %v, want true", body["stream"])
	}
	if so, ok := body["stream_options"].(map[string]any); !ok || so["include_usage"] != true {
		t.Errorf("body.stream_options.include_usage missing or wrong: %v", body["stream_options"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("body.messages len = %d, want 2 (system + user)", len(msgs))
	}
	if m0 := msgs[0].(map[string]any); m0["role"] != "system" || m0["content"] != "You are helpful." {
		t.Errorf("system message wrong: %+v", m0)
	}
	if m1 := msgs[1].(map[string]any); m1["role"] != "user" || m1["content"] != "say hi" {
		t.Errorf("user message wrong: %+v", m1)
	}
}

func TestStream_HTTPErrorReturnsSetupError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-bad", BaseURL: srv.URL})
	_, err := p.Stream(context.Background(), provider.Request{Model: "openai/gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("error should describe the HTTP failure: %v", err)
	}
}

func TestStream_RejectsMissingAPIKey(t *testing.T) {
	p := New(Options{})
	_, err := p.Stream(context.Background(), provider.Request{Model: "openai/gpt-4o-mini"})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("expected API key error, got %v", err)
	}
}

func TestStream_RejectsWrongProvider(t *testing.T) {
	p := New(Options{APIKey: "sk-test"})
	_, err := p.Stream(context.Background(), provider.Request{Model: "anthropic/claude-opus-4-7"})
	if err == nil || !strings.Contains(err.Error(), "not an openai model") {
		t.Errorf("expected wrong-provider error, got %v", err)
	}
}

func TestStream_MidStreamErrorEmitsErrorDelta(t *testing.T) {
	const broken = `data: {"choices":[{"delta":{"content":"partial"}}]}

data: {malformed json

data: [DONE]

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(broken))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), provider.Request{Model: "openai/gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	var got []provider.Delta
	for d := range ch {
		got = append(got, d)
	}
	// Expect: DeltaText("partial"), DeltaError(parse), then close.
	if len(got) != 2 {
		t.Fatalf("got %d deltas, want 2: %+v", len(got), got)
	}
	if got[0].Kind != provider.DeltaText || got[0].Text != "partial" {
		t.Errorf("first delta = %+v, want DeltaText 'partial'", got[0])
	}
	if got[1].Kind != provider.DeltaError || got[1].Err == nil {
		t.Errorf("second delta should be DeltaError with non-nil Err: %+v", got[1])
	}
}

func TestLoadAPIKey_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":"sk-real"},
			"deepseek:default": {"type":"api_key","provider":"deepseek","key":"sk-ds"}
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAPIKey(path, "openai:default")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-real" {
		t.Errorf("LoadAPIKey = %q, want %q", got, "sk-real")
	}
}

func TestLoadAPIKey_RejectsWrongProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{"version":1,"profiles":{"openai:weird":{"type":"api_key","provider":"deepseek","key":"sk-x"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAPIKey(path, "openai:weird")
	if err == nil || !strings.Contains(err.Error(), "want openai") {
		t.Errorf("expected provider mismatch error, got %v", err)
	}
}

func TestLoadAPIKey_RejectsOAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{"version":1,"profiles":{"openai:default":{"type":"oauth","provider":"openai"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAPIKey(path, "openai:default")
	if err == nil || !strings.Contains(err.Error(), "want api_key") {
		t.Errorf("expected type mismatch error, got %v", err)
	}
}

func TestLoadAPIKey_MissingProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{"version":1,"profiles":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAPIKey(path, "openai:default")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

