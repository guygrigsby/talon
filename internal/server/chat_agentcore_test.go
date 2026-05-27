package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestShouldUseAgentcoreFor_RoutingMatrix(t *testing.T) {
	// h.agentcoreRun is set so the no-runner short-circuit doesn't fire.
	h := &ChatHandler{agentcoreRun: noopRunner}

	cases := []struct {
		modelID  string
		wantPath string // "agentcore" or "legacy"
	}{
		{"openai/gpt-4o", "agentcore"},
		{"openai/gpt-4o-mini", "agentcore"},
		{"openai/gpt-4.1-mini", "agentcore"},
		{"openai/gpt-5", "agentcore"},
		{"openai/gpt-5.4", "agentcore"},
		{"openai/gpt-5.4-mini", "agentcore"},
		{"openai/gpt-5.4-nano", "agentcore"},
		{"openai/gpt-5.5", "agentcore"},
		{"anthropic/claude-opus-4-7", "agentcore"},
		{"anthropic/claude-haiku-4-5", "agentcore"},
		{"deepseek/deepseek-chat", "agentcore"},
		{"deepseek/deepseek-reasoner", "agentcore"},
		{"mistral/mistral-large-3-25-12", "agentcore"},
		{"mistral/mistral-small-4-0-26-03", "agentcore"},
		{"mlx/llama-3-8b", "agentcore"},
		{"lmstudio/qwen-2.5-32b", "agentcore"},
		{"ollama/llama3", "agentcore"},
	}
	for _, c := range cases {
		got := "legacy"
		if h.shouldUseAgentcoreFor(c.modelID) {
			got = "agentcore"
		}
		if got != c.wantPath {
			t.Errorf("shouldUseAgentcoreFor(%q) = %s, want %s", c.modelID, got, c.wantPath)
		}
	}
}

func TestShouldUseAgentcoreFor_NoRunnerWiredAlwaysLegacy(t *testing.T) {
	h := &ChatHandler{agentcoreRun: nil}
	for _, m := range []string{"openai/gpt-4o-mini", "deepseek/deepseek-chat", "mistral/something"} {
		if h.shouldUseAgentcoreFor(m) {
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

func TestRunStreamAgentcorePassesSessionSelectedModel(t *testing.T) {
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
		agentcoreRun: func(
			_ context.Context,
			_ string, _ string, _ string, _ string, selectedModelID string,
			_ []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			_ func(string, string, string, bool),
			_ func(int, string, string),
		) (AgentcoreRunResult, error) {
			gotOverride = selectedModelID
			return AgentcoreRunResult{FinalText: "done"}, nil
		},
	}

	h.runStreamAgentcore("run_1", sessionKey, "main", "hello", nil, sessionKey+"|run_1")
	if gotOverride != override {
		t.Fatalf("model override = %q, want %q", gotOverride, override)
	}
	history := h.store.Snapshot(sessionKey)
	if len(history) != 1 || history[0].Role != "assistant" || history[0].Content != "done" {
		t.Fatalf("history = %+v", history)
	}
}

func TestHandleSendViaAgentcorePassesPriorHistory(t *testing.T) {
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
		agentcoreRun: func(
			_ context.Context,
			_ string, _ string, _ string, userText string, _ string,
			priorHistory []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			_ func(string, string, string, bool),
			_ func(int, string, string),
		) (AgentcoreRunResult, error) {
			got <- captured{
				userText: userText,
				prior:    append([]ChatMessage(nil), priorHistory...),
			}
			return AgentcoreRunResult{}, nil
		},
	}

	_, ferr := h.handleSendViaAgentcore(t.Context(), chatSendParams{
		SessionKey:     sessionKey,
		Message:        "what color did I say?",
		IdempotencyKey: "run_1",
	}, "main")
	if ferr != nil {
		t.Fatalf("handleSendViaAgentcore error: %+v", ferr)
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
		t.Fatal("agentcore runner was not called")
	}

	history := store.Snapshot(sessionKey)
	if len(history) < 3 || history[2].Role != "user" || history[2].Content != "what color did I say?" {
		t.Fatalf("stored history after send = %+v", history)
	}
}

func TestRunStreamAgentcoreRecordsUsageCost(t *testing.T) {
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
		agentcoreRun: func(
			_ context.Context,
			_ string, _ string, _ string, _ string, _ string,
			_ []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			_ func(string, string, string, bool),
			_ func(int, string, string),
		) (AgentcoreRunResult, error) {
			return AgentcoreRunResult{
				ModelID: "deepseek/deepseek-chat",
				Usage:   AgentcoreUsage{InputTokens: 20_000},
			}, nil
		},
	}

	h.runStreamAgentcore("run_1", "agent:main:web", "main", "hello", nil, "agent:main:web|run_1")
	if err := costs.Allow("main"); err == nil {
		t.Fatal("expected recorded agentcore usage to trip the daily cost cap")
	}
}

func TestRunStreamAgentcorePreservesToolResultErrorFlag(t *testing.T) {
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
		agentcoreRun: func(
			_ context.Context,
			_ string, _ string, _ string, _ string, _ string,
			_ []ChatMessage,
			_ func(int, string, string, string),
			_ func(string, string, string),
			emitToolResult func(string, string, string, bool),
			_ func(int, string, string),
		) (AgentcoreRunResult, error) {
			emitToolResult("call_a", "bash", "failed", true)
			return AgentcoreRunResult{}, nil
		},
	}

	h.runStreamAgentcore("run_1", sessionKey, "main", "hello", nil, sessionKey+"|run_1")
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

// noopRunner is a no-op AgentcoreRunFn so tests can populate
// h.agentcoreRun without doing real work.
var noopRunner AgentcoreRunFn = func(
	_ context.Context,
	_ string, _ string, _ string, _ string, _ string,
	_ []ChatMessage,
	_ func(int, string, string, string),
	_ func(string, string, string),
	_ func(string, string, string, bool),
	_ func(int, string, string),
) (AgentcoreRunResult, error) {
	return AgentcoreRunResult{}, nil
}
