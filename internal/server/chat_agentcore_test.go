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

func TestRunStreamAgentcorePassesSessionModelOverride(t *testing.T) {
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
			_ string, _ string, _ string, _ string, modelOverride string,
			_ func(int, string, string, string),
			_ func(string, string, string),
			_ func(string, string, string, bool),
			_ func(int, string, string),
		) (string, error) {
			gotOverride = modelOverride
			return "done", nil
		},
	}

	h.runStreamAgentcore("run_1", sessionKey, "main", "hello", sessionKey+"|run_1")
	if gotOverride != override {
		t.Fatalf("model override = %q, want %q", gotOverride, override)
	}
	history := h.store.Snapshot(sessionKey)
	if len(history) != 1 || history[0].Role != "assistant" || history[0].Content != "done" {
		t.Fatalf("history = %+v", history)
	}
}

// noopRunner is a no-op AgentcoreRunFn so tests can populate
// h.agentcoreRun without doing real work.
var noopRunner AgentcoreRunFn = func(
	_ context.Context,
	_ string, _ string, _ string, _ string, _ string,
	_ func(int, string, string, string),
	_ func(string, string, string),
	_ func(string, string, string, bool),
	_ func(int, string, string),
) (string, error) {
	return "", nil
}
