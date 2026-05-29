package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestShouldUseChatRunnerFor_RoutingMatrix(t *testing.T) {
	// h.chatRun is set so the no-runner short-circuit doesn't fire.
	h := &ChatHandler{chatRun: noopRunner}

	cases := []struct {
		modelID  string
		wantPath string // "chat-runner" or "legacy"
	}{
		{"openai/gpt-4o", "chat-runner"},
		{"openai/gpt-4o-mini", "chat-runner"},
		{"openai/gpt-4.1-mini", "chat-runner"},
		{"openai/gpt-5", "chat-runner"},
		{"openai/gpt-5.4", "chat-runner"},
		{"openai/gpt-5.4-mini", "chat-runner"},
		{"openai/gpt-5.4-nano", "chat-runner"},
		{"openai/gpt-5.5", "chat-runner"},
		{"anthropic/claude-opus-4-7", "chat-runner"},
		{"anthropic/claude-haiku-4-5", "chat-runner"},
		{"deepseek/deepseek-chat", "chat-runner"},
		{"deepseek/deepseek-reasoner", "chat-runner"},
		{"mistral/mistral-large-3-25-12", "chat-runner"},
		{"mistral/mistral-small-4-0-26-03", "chat-runner"},
		{"mlx/llama-3-8b", "chat-runner"},
		{"lmstudio/qwen-2.5-32b", "chat-runner"},
		{"ollama/llama3", "chat-runner"},
	}
	for _, c := range cases {
		got := "legacy"
		if h.shouldUseChatRunnerFor(c.modelID) {
			got = "chat-runner"
		}
		if got != c.wantPath {
			t.Errorf("shouldUseChatRunnerFor(%q) = %s, want %s", c.modelID, got, c.wantPath)
		}
	}
}

func TestShouldUseChatRunnerFor_NoRunnerWiredAlwaysLegacy(t *testing.T) {
	h := &ChatHandler{chatRun: nil}
	for _, m := range []string{"openai/gpt-4o-mini", "deepseek/deepseek-chat", "mistral/something"} {
		if h.shouldUseChatRunnerFor(m) {
			t.Errorf("no runner wired → should be legacy for %q", m)
		}
	}
}

func TestProviderModelID_ProviderExtraction(t *testing.T) {
	cases := map[string]string{
		"openai/gpt-4o-mini":          "openai",
		"deepseek/deepseek-chat":      "deepseek",
		"anthropic/claude-opus-4-7":   "anthropic",
		"":                            "",
		"no-slash":                    "",
		"openai/gpt-4o/extra-segment": "openai",
	}
	for in, want := range cases {
		if got := providerModelID(in).Provider(); got != want {
			t.Errorf("providerModelID(%q).Provider() = %q, want %q", in, got, want)
		}
	}
}

func TestRunChatStreamPassesSessionSelectedModel(t *testing.T) {
	const (
		sessionKey = "agent:main:web"
		override   = "deepseek/deepseek-chat"
	)
	sessions := NewSessionStore()
	sessions.Patch(sessionKey, map[string]json.RawMessage{
		"model": json.RawMessage(`"` + override + `"`),
	})

	var gotOverride string
	h := &ChatHandler{
		store:         NewChatStore(),
		sessions:      sessions,
		sinks:         NewSinkRegistry(),
		runs:          make(map[string]string),
		StreamTimeout: time.Second,
		chatRun: func(
			_ context.Context,
			_ string, _ string, _ string, _ string, selectedModelID string,
			_ []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			_ func(string, string, string, bool),
			_ func(int, string, string),
		) (ChatRunResult, error) {
			gotOverride = selectedModelID
			return ChatRunResult{FinalText: "done"}, nil
		},
	}

	h.runChatStream("run_1", sessionKey, "main", "hello", nil, sessionKey+"|run_1")
	if gotOverride != override {
		t.Fatalf("model override = %q, want %q", gotOverride, override)
	}
	history := h.store.Snapshot(sessionKey)
	if len(history) != 1 || history[0].Role != "assistant" || history[0].Content != "done" {
		t.Fatalf("history = %+v", history)
	}
}

func TestHandleSendViaChatRunnerPassesPriorHistory(t *testing.T) {
	const sessionKey = "agent:main:web"
	store := NewChatStore()
	store.Append(sessionKey, "user", "my favorite color is blue")
	store.Append(sessionKey, "assistant", "noted")

	type captured struct {
		userText string
		prior    []ChatMessage
	}
	got := make(chan captured, 1)
	h := &ChatHandler{
		store:         store,
		sinks:         NewSinkRegistry(),
		runs:          make(map[string]string),
		StreamTimeout: time.Second,
		chatRun: func(
			_ context.Context,
			_ string, _ string, _ string, userText string, _ string,
			priorHistory []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			_ func(string, string, string, bool),
			_ func(int, string, string),
		) (ChatRunResult, error) {
			got <- captured{
				userText: userText,
				prior:    append([]ChatMessage(nil), priorHistory...),
			}
			return ChatRunResult{}, nil
		},
	}

	_, ferr := h.handleSendViaChatRunner(t.Context(), chatSendParams{
		SessionKey:     sessionKey,
		Message:        "what color did I say?",
		IdempotencyKey: "run_1",
	}, "main")
	if ferr != nil {
		t.Fatalf("handleSendViaChatRunner error: %+v", ferr)
	}

	select {
	case cap := <-got:
		if cap.userText != "what color did I say?" {
			t.Fatalf("userText = %q", cap.userText)
		}
		if len(cap.prior) != 2 {
			t.Fatalf("prior len = %d, want 2: %+v", len(cap.prior), cap.prior)
		}
		if cap.prior[0].Role != "user" || cap.prior[0].Content != "my favorite color is blue" {
			t.Fatalf("prior[0] = %+v", cap.prior[0])
		}
		if cap.prior[1].Role != "assistant" || cap.prior[1].Content != "noted" {
			t.Fatalf("prior[1] = %+v", cap.prior[1])
		}
	case <-time.After(time.Second):
		t.Fatal("chat runner was not called")
	}

	history := store.Snapshot(sessionKey)
	if len(history) < 3 || history[2].Role != "user" || history[2].Content != "what color did I say?" {
		t.Fatalf("stored history after send = %+v", history)
	}
}

func TestRunChatStreamRecordsUsageCost(t *testing.T) {
	paths := readFixture(t, `{
		"agents":{"defaults":{"dailyUsdCap":1.00}},
		"models":{"deepseek/deepseek-chat":{"priceUsdPer1M":{"in":100.0,"out":100.0}}}
	}`)
	costs := NewCostTracker(paths)
	h := &ChatHandler{
		store:         NewChatStore(),
		sinks:         NewSinkRegistry(),
		runs:          make(map[string]string),
		StreamTimeout: time.Second,
		costs:         costs,
		chatRun: func(
			_ context.Context,
			_ string, _ string, _ string, _ string, _ string,
			_ []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			_ func(string, string, string, bool),
			_ func(int, string, string),
		) (ChatRunResult, error) {
			return ChatRunResult{
				ModelID: "deepseek/deepseek-chat",
				Usage:   ChatUsage{InputTokens: 20_000},
			}, nil
		},
	}

	h.runChatStream("run_1", "agent:main:web", "main", "hello", nil, "agent:main:web|run_1")
	if err := costs.Allow("main"); err == nil {
		t.Fatal("expected recorded chat runner usage to trip the daily cost cap")
	}
}

func TestRunChatStreamPreservesToolResultErrorFlag(t *testing.T) {
	const sessionKey = "agent:main:web"
	sinks := NewSinkRegistry()
	sink := &captureSink{}
	unsub := sinks.Subscribe(sessionKey, sink)
	defer unsub()
	h := &ChatHandler{
		store:         NewChatStore(),
		sinks:         sinks,
		runs:          make(map[string]string),
		StreamTimeout: time.Second,
		chatRun: func(
			_ context.Context,
			_ string, _ string, _ string, _ string, _ string,
			_ []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			emitToolResult func(string, string, string, bool),
			_ func(int, string, string),
		) (ChatRunResult, error) {
			emitToolResult("call_a", "bash", "failed", true)
			return ChatRunResult{}, nil
		},
	}

	h.runChatStream("run_1", sessionKey, "main", "hello", nil, sessionKey+"|run_1")
	if sink.count() != 1 {
		t.Fatalf("got %d events, want 1", sink.count())
	}
	payload, ok := sink.events[0].payload.(AgentEventPayload)
	if !ok {
		t.Fatalf("payload type = %T, want AgentEventPayload", sink.events[0].payload)
	}
	if payload.Data["isError"] != true {
		t.Fatalf("isError = %+v, want true in payload %+v", payload.Data["isError"], payload.Data)
	}
}

// noopRunner is a no-op ChatRunFn so tests can populate
// h.chatRun without doing real work.
var noopRunner ChatRunFn = func(
	_ context.Context,
	_ string, _ string, _ string, _ string, _ string,
	_ []ChatMessage,
	_ func(int, string, string, string),
	_ func(string, string, string),
	_ func(string, string, string, bool),
	_ func(int, string, string),
) (ChatRunResult, error) {
	return ChatRunResult{}, nil
}
