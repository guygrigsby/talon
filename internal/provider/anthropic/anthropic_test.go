package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/provider"
)

// recordedTextSSE is a paste-shaped Anthropic streaming response with
// two text chunks plus message_start/stop framing. Whitespace + blank-
// line framing match what api.anthropic.com actually emits.
const recordedTextSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":12,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`

// recordedToolUseSSE simulates a tool_use turn: a text preamble plus
// one tool_use block whose JSON input arrives as input_json_delta
// fragments and is finalized at content_block_stop.
const recordedToolUseSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_02","role":"assistant","content":[],"model":"claude-opus-4-7","usage":{"input_tokens":40,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"calling tool"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_99","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":":\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":17}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStream_HappyPath(t *testing.T) {
	var capturedBody []byte
	var capturedKey, capturedVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		capturedKey = r.Header.Get("x-api-key")
		capturedVer = r.Header.Get("anthropic-version")
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedTextSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := p.Stream(ctx, provider.Request{
		Model:    "anthropic/claude-opus-4-7",
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
	if capturedKey != "sk-test" {
		t.Errorf("x-api-key = %q, want sk-test", capturedKey)
	}
	if capturedVer != DefaultAPIVersion {
		t.Errorf("anthropic-version = %q, want %q", capturedVer, DefaultAPIVersion)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["model"] != "claude-opus-4-7" {
		t.Errorf("body.model = %v", body["model"])
	}
	if body["system"] != "You are helpful." {
		t.Errorf("body.system = %v", body["system"])
	}
	if body["stream"] != true {
		t.Errorf("body.stream = %v", body["stream"])
	}
	// Anthropic requires max_tokens; the provider must surface it.
	if mt, _ := body["max_tokens"].(float64); int(mt) != 50 {
		t.Errorf("body.max_tokens = %v, want 50", body["max_tokens"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("body.messages len = %d, want 1 (system is top-level)", len(msgs))
	}
	if m0 := msgs[0].(map[string]any); m0["role"] != "user" || m0["content"] != "say hi" {
		t.Errorf("user message wrong: %+v", m0)
	}
}

func TestStream_ToolUseAccumulatesInputJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedToolUseSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-test", BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), provider.Request{
		Model:    "anthropic/claude-opus-4-7",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "weather"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var (
		text  strings.Builder
		calls []provider.ToolCall
		usage *provider.Usage
	)
	for d := range ch {
		switch d.Kind {
		case provider.DeltaText:
			text.WriteString(d.Text)
		case provider.DeltaToolCall:
			calls = append(calls, *d.ToolCall)
		case provider.DeltaUsage:
			usage = d.Usage
		case provider.DeltaError:
			t.Fatalf("error delta: %v", d.Err)
		}
	}
	if text.String() != "calling tool" {
		t.Errorf("text = %q", text.String())
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want 1", calls)
	}
	if calls[0].ID != "toolu_99" || calls[0].Name != "get_weather" {
		t.Errorf("call shape wrong: %+v", calls[0])
	}
	// The two input_json_delta fragments must concatenate into a
	// single valid JSON object.
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].ArgumentsJSON), &args); err != nil {
		t.Fatalf("ArgumentsJSON not valid JSON (%q): %v", calls[0].ArgumentsJSON, err)
	}
	if args["city"] != "Paris" {
		t.Errorf("args = %+v, want city=Paris", args)
	}
	if usage == nil || usage.InputTokens != 40 || usage.OutputTokens != 17 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestBuildRequestBody_ToolResultEmitsUserToolResultBlock(t *testing.T) {
	body, err := buildRequestBody("claude-opus-4-7", provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "weather?"},
			{Role: provider.RoleAssistant, Content: "calling", ToolCalls: []provider.ToolCall{
				{ID: "toolu_1", Name: "get_weather", ArgumentsJSON: `{"city":"Paris"}`},
			}},
			{Role: provider.RoleTool, ToolCallID: "toolu_1", Content: "22C sunny"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(msgs), msgs)
	}
	// 0: user text — string content.
	if m := msgs[0].(map[string]any); m["role"] != "user" || m["content"] != "weather?" {
		t.Errorf("messages[0] = %+v", m)
	}
	// 1: assistant content array with text + tool_use blocks.
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("messages[1].role = %v", asst["role"])
	}
	blocks := asst["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("assistant content blocks = %d, want 2", len(blocks))
	}
	if b0 := blocks[0].(map[string]any); b0["type"] != "text" || b0["text"] != "calling" {
		t.Errorf("text block = %+v", b0)
	}
	if b1 := blocks[1].(map[string]any); b1["type"] != "tool_use" || b1["id"] != "toolu_1" || b1["name"] != "get_weather" {
		t.Errorf("tool_use block = %+v", b1)
	}
	// 2: tool result → role=user, content=[{type:tool_result, ...}].
	tr := msgs[2].(map[string]any)
	if tr["role"] != "user" {
		t.Errorf("tool-result role = %v, want user (anthropic spec)", tr["role"])
	}
	trBlocks := tr["content"].([]any)
	if b := trBlocks[0].(map[string]any); b["type"] != "tool_result" || b["tool_use_id"] != "toolu_1" || b["content"] != "22C sunny" {
		t.Errorf("tool_result block = %+v", b)
	}
}

func TestStream_HTTPErrorReturnsSetupError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	p := New(Options{APIKey: "sk-bad", BaseURL: srv.URL})
	_, err := p.Stream(context.Background(), provider.Request{Model: "anthropic/claude-opus-4-7"})
	if err == nil {
		t.Fatal("expected error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("error should describe HTTP failure: %v", err)
	}
}

func TestStream_RejectsMissingAPIKey(t *testing.T) {
	p := New(Options{})
	_, err := p.Stream(context.Background(), provider.Request{Model: "anthropic/claude-opus-4-7"})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("want API-key error, got %v", err)
	}
}

func TestStream_RejectsWrongProviderPrefix(t *testing.T) {
	p := New(Options{APIKey: "sk-test"})
	_, err := p.Stream(context.Background(), provider.Request{Model: "openai/gpt-4o"})
	if err == nil || !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("want provider-mismatch error, got %v", err)
	}
}
