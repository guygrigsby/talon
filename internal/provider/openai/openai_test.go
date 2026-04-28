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
	if err == nil || !strings.Contains(err.Error(), "does not target provider") {
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

// --- tool calling ---------------------------------------------------------

// recordedToolCallSSE has the model emit one tool_call (bash with
// "ls /tmp") whose arguments arrive across three chunks, then finish_reason
// = tool_calls, then [DONE]. Verbatim shape matches what OpenAI's API
// returns when tools are configured.
const recordedToolCallSSE = `data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"bash","arguments":""}}]}}]}

data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}

data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls /tmp\"}"}}]}}]}

data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func TestStream_ToolCallAccumulation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedToolCallSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), provider.Request{
		Model: "openai/gpt-4o-mini",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "list /tmp"},
		},
		Tools: []provider.ToolSpec{
			{
				Name:             "bash",
				Description:      "Execute a shell command",
				ParametersSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls []*provider.ToolCall
	for d := range ch {
		switch d.Kind {
		case provider.DeltaToolCall:
			calls = append(calls, d.ToolCall)
		case provider.DeltaError:
			t.Fatalf("unexpected error: %v", d.Err)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	c := calls[0]
	if c.ID != "call_abc" {
		t.Errorf("id = %q, want call_abc", c.ID)
	}
	if c.Name != "bash" {
		t.Errorf("name = %q, want bash", c.Name)
	}
	if c.ArgumentsJSON != `{"command":"ls /tmp"}` {
		t.Errorf("args = %q, want {\"command\":\"ls /tmp\"}", c.ArgumentsJSON)
	}
}

func TestStream_RequestBodyIncludesTools(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), provider.Request{
		Model: "openai/gpt-4o-mini",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "hi"},
		},
		Tools: []provider.ToolSpec{
			{
				Name:             "read",
				Description:      "Read a file",
				ParametersSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatal(err)
	}
	tools, ok := body["tools"].([]any)
	if !ok {
		t.Fatalf("body.tools missing or wrong type: %v", body["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("body.tools len = %d, want 1", len(tools))
	}
	t0 := tools[0].(map[string]any)
	if t0["type"] != "function" {
		t.Errorf("tools[0].type = %v, want function", t0["type"])
	}
	fn := t0["function"].(map[string]any)
	if fn["name"] != "read" || fn["description"] != "Read a file" {
		t.Errorf("tools[0].function = %+v", fn)
	}
}

func TestStream_RequestBodyOmitsToolsWhenAbsent(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ch, _ := p.Stream(context.Background(), provider.Request{
		Model: "openai/gpt-4o-mini", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	for range ch {
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["tools"]; present {
		t.Errorf("body.tools should be omitted when no tools are passed: %+v", body["tools"])
	}
}

func TestStream_EmptyContentIsExplicitStringNotNull(t *testing.T) {
	// OpenAI rejects messages with content:null ("expected a string, got
	// null"). Tool result turns where the tool returned "" and assistant
	// turns that only carry tool_calls both used to drop the field via
	// omitempty — that surfaced as null on OpenAI's side. The contract:
	// always emit content as a string, even if empty.
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ch, _ := p.Stream(context.Background(), provider.Request{
		Model: "openai/gpt-4o-mini",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "do thing"},
			// Assistant turn that emitted ONLY tool_calls (no text).
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "call_a", Name: "glob", ArgumentsJSON: `{"pattern":"*"}`},
				},
			},
			// Tool result with empty output (e.g. silent command).
			{Role: provider.RoleTool, ToolCallID: "call_a", Content: ""},
		},
	})
	for range ch {
	}

	// Re-parse the body and verify content is the string "" — never
	// missing, never null.
	if !strings.Contains(string(capturedBody), `"content":""`) {
		t.Errorf("expected `\"content\":\"\"` in body for empty-content turns, got: %s", capturedBody)
	}
	if strings.Contains(string(capturedBody), `"content":null`) {
		t.Errorf("content should never serialize as null: %s", capturedBody)
	}

	// Sanity: parse back and confirm every message has a string content.
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatal(err)
	}
	for i, raw := range body["messages"].([]any) {
		msg := raw.(map[string]any)
		c, present := msg["content"]
		if !present {
			t.Errorf("messages[%d] missing content field: %+v", i, msg)
			continue
		}
		if _, ok := c.(string); !ok {
			t.Errorf("messages[%d].content is %T, want string: %+v", i, c, c)
		}
	}
}

func TestStream_RequestBodySerializesAssistantToolCallsAndToolResults(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ch, _ := p.Stream(context.Background(), provider.Request{
		Model: "openai/gpt-4o-mini",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "list /tmp"},
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "call_abc", Name: "bash", ArgumentsJSON: `{"command":"ls /tmp"}`},
				},
			},
			{Role: provider.RoleTool, ToolCallID: "call_abc", Content: "file1\nfile2\n"},
		},
	})
	for range ch {
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d, want 3", len(msgs))
	}
	// Assistant turn carries tool_calls, no content field (or empty).
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("msgs[1].role = %v", asst["role"])
	}
	tcalls := asst["tool_calls"].([]any)
	if len(tcalls) != 1 {
		t.Fatalf("tool_calls len = %d", len(tcalls))
	}
	tc0 := tcalls[0].(map[string]any)
	if tc0["id"] != "call_abc" || tc0["type"] != "function" {
		t.Errorf("tool_calls[0] = %+v", tc0)
	}
	tcfn := tc0["function"].(map[string]any)
	if tcfn["name"] != "bash" || tcfn["arguments"] != `{"command":"ls /tmp"}` {
		t.Errorf("tool_calls[0].function = %+v", tcfn)
	}
	// Tool result turn.
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" {
		t.Errorf("msgs[2].role = %v, want tool", tool["role"])
	}
	if tool["tool_call_id"] != "call_abc" {
		t.Errorf("msgs[2].tool_call_id = %v", tool["tool_call_id"])
	}
	if tool["content"] != "file1\nfile2\n" {
		t.Errorf("msgs[2].content = %q", tool["content"])
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


// --- LoadProfileKeyOptional -------------------------------------------------

func TestLoadProfileKeyOptional_NoFileReturnsEmpty(t *testing.T) {
	got, err := LoadProfileKeyOptional(filepath.Join(t.TempDir(), "missing.json"), "lmstudio:default", "lmstudio")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty for missing file", got)
	}
}

func TestLoadProfileKeyOptional_NoProfileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{"version":1,"profiles":{"openai:default":{"type":"api_key","provider":"openai","key":"sk-real"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfileKeyOptional(path, "lmstudio:default", "lmstudio")
	if err != nil {
		t.Fatalf("missing profile should not error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty for missing profile", got)
	}
}

func TestLoadProfileKeyOptional_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{"version":1,"profiles":{"lmstudio:default":{"type":"api_key","provider":"lmstudio","key":"k-1"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfileKeyOptional(path, "lmstudio:default", "lmstudio")
	if err != nil {
		t.Fatal(err)
	}
	if got != "k-1" {
		t.Errorf("got %q, want k-1", got)
	}
}

func TestLoadProfileKeyOptional_MalformedProfileStillErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{"version":1,"profiles":{"lmstudio:default":{"type":"api_key","provider":"lmstudio","key":""}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfileKeyOptional(path, "lmstudio:default", "lmstudio")
	if err == nil || !strings.Contains(err.Error(), "empty key") {
		t.Errorf("empty-key profile should error, got %v", err)
	}
}
