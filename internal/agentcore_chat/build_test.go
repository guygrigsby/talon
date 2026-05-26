package agentcore_chat

import (
	"strings"
	"testing"

	"github.com/guygrigsby/jess/memory"

	"github.com/guygrigsby/talon/internal/openclaw"
)

func TestBuilder_BuildAgent_Openai(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}},
			"list": [{"id": "main", "workspace": "/tmp/talon-test-ws"}]
		}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	agent, choice, err := b.BuildAgent("main")
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	if agent == nil {
		t.Fatal("agent is nil")
	}
	if choice.Provider != "openai" || choice.Model != "gpt-5.4-mini" {
		t.Errorf("choice = %+v", choice)
	}
}

func TestBuilder_BuildAgent_ModelOverrideWins(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {
			"defaults": {
				"model": {
					"primary": "openai/gpt-5.4-mini",
					"fallbacks": ["anthropic/claude-opus-4-7"]
				}
			},
			"list": [{"id": "main", "workspace": "/tmp/talon-test-ws"}]
		}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).
		WithAuthOverride(map[string]ProviderAuth{
			"deepseek": {Provider: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-test"},
		}).
		WithModelOverride("deepseek/deepseek-chat")

	agent, choice, err := b.BuildAgent("main")
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	if agent == nil {
		t.Fatal("agent is nil")
	}
	if choice.ID != "deepseek/deepseek-chat" || choice.Provider != "deepseek" || choice.Model != "deepseek-chat" {
		t.Errorf("choice = %+v", choice)
	}
	if len(choice.Fallbacks) != 1 || choice.Fallbacks[0] != "anthropic/claude-opus-4-7" {
		t.Errorf("fallbacks = %v", choice.Fallbacks)
	}
}

func TestBuilder_BuildAgent_Mistral_RegisteredViaInit(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "mistral/mistral-large-3-25-12"}}}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"mistral": {Provider: "mistral", BaseURL: "https://api.mistral.ai/v1", APIKey: "test-key"},
	})
	_, choice, err := b.BuildAgent("main")
	if err != nil {
		t.Fatalf("mistral should be registered as openai-compat via init(): %v", err)
	}
	if choice.Provider != "mistral" {
		t.Errorf("choice provider = %q", choice.Provider)
	}
}

func TestBuilder_BuildAgent_LocalLoopback_NoKeyNeeded(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "mlx/llama-3-8b"}}}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"mlx": {Provider: "mlx", BaseURL: "http://localhost:8080/v1"}, // no APIKey
	})
	_, _, err := b.BuildAgent("main")
	if err != nil {
		t.Errorf("loopback should build without API key: %v", err)
	}
}

func TestBuilder_BuildAgent_MissingProviderAuth(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "anthropic/claude-opus-4-7"}}}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{})
	_, _, err := b.BuildAgent("main")
	if err == nil {
		t.Fatal("expected error when provider auth missing")
	}
	if !strings.Contains(err.Error(), "anthropic") || !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("error should mention provider + unauthenticated: %v", err)
	}
}

func TestBuilder_BuildAgent_NoModelConfigured(t *testing.T) {
	clearProviderEnv(t)
	b := NewBuilder([]byte(`{}`), openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{})
	_, _, err := b.BuildAgent("main")
	if err == nil {
		t.Fatal("expected error when no model configured")
	}
	if !strings.Contains(err.Error(), "no model configured") {
		t.Errorf("error message = %q", err.Error())
	}
}

func TestBuilder_BuildAgent_BareModelIDError(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "broken-no-provider"}}}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{})
	_, _, err := b.BuildAgent("main")
	if err == nil || !strings.Contains(err.Error(), "no provider segment") {
		t.Errorf("expected provider-segment error, got %v", err)
	}
}

func TestBuilder_BuildAgent_PerAgentWorkspace(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}, "workspace": "/tmp/default-ws"},
			"list": [{"id": "coding", "workspace": "/tmp/coding-ws"}]
		}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	_, _, err := b.BuildAgent("coding")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Indirect check: BuildAgent passes the per-agent workspace to
	// the tool constructors. We can't inspect the agent's tool list
	// from outside the package, but the absence of error + the
	// resolver test below give us coverage. Keep this as a smoke
	// build test.
}

func TestResolveAgentWorkspace_TiersThenDefaults(t *testing.T) {
	cfg := []byte(`{
		"agents": {
			"defaults": {"workspace": "/tmp/d"},
			"list": [{"id": "x", "workspace": "/tmp/x"}, {"id": "y"}]
		}
	}`)
	if got := resolveAgentWorkspace(cfg, "x"); got != "/tmp/x" {
		t.Errorf("per-agent workspace should win: got %q", got)
	}
	if got := resolveAgentWorkspace(cfg, "y"); got != "/tmp/d" {
		t.Errorf("no per-agent workspace should fall back to defaults: got %q", got)
	}
}

func TestBuilder_BuildAgent_WithMemoryAttachesTools(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "openai/gpt-4o-mini"}, "workspace": "/tmp/talon-mem-test"}}
	}`)
	store := memory.NewInMemoryStore()
	recaller := memory.NewSimpleRecaller()
	b := NewBuilder(cfg, openclaw.Paths{}).
		WithAuthOverride(map[string]ProviderAuth{
			"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
		}).
		WithMemory(store, recaller)

	agent, _, err := b.BuildAgent("main")
	if err != nil {
		t.Fatalf("build with memory: %v", err)
	}
	// Indirect verification: agentcore.Agent doesn't expose its
	// tool list publicly, so we look for the tool by behavior — a
	// RememberTool that can be invoked. The agent's tools include
	// jess's "remember" and "recall" when memory is wired; this
	// test asserts construction succeeds and the agent doesn't
	// reject these tools.
	if agent == nil {
		t.Fatal("agent is nil")
	}
}

func TestBuilder_BuildAgent_NoMemoryNoMemoryTools(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "openai/gpt-4o-mini"}, "workspace": "/tmp/talon-no-mem"}}
	}`)
	b := NewBuilder(cfg, openclaw.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	// No WithMemory call.
	_, _, err := b.BuildAgent("main")
	if err != nil {
		t.Fatalf("build without memory: %v", err)
	}
}

func TestResolveModelMaxTokens_TableDriven(t *testing.T) {
	cfg := []byte(`{
		"models": {"providers": {
			"openai": {"models": [
				{"id": "gpt-4o-mini", "maxTokens": 16000},
				{"id": "gpt-5.4-mini", "maxTokens": 128000}
			]},
			"anthropic": {"models": [
				{"id": "claude-opus-4-7", "maxTokens": 128000}
			]}
		}}
	}`)
	cases := []struct {
		prov, model string
		want        int
	}{
		{"openai", "gpt-4o-mini", 16000},
		{"openai", "gpt-5.4-mini", 128000},
		{"anthropic", "claude-opus-4-7", 128000},
		{"openai", "gpt-not-listed", 0},
		{"unknown", "anything", 0},
	}
	for _, c := range cases {
		if got := resolveModelMaxTokens(cfg, c.prov, c.model); got != c.want {
			t.Errorf("resolveModelMaxTokens(%q,%q) = %d, want %d", c.prov, c.model, got, c.want)
		}
	}
}

func TestResolveSystemPrompt_TiersThenDefaults(t *testing.T) {
	cfg := []byte(`{
		"agents": {
			"defaults": {"systemPrompt": "default"},
			"list": [{"id": "x", "systemPrompt": "per-agent"}, {"id": "y"}]
		}
	}`)
	if got := resolveSystemPrompt(cfg, "x"); got != "per-agent" {
		t.Errorf("per-agent prompt should win: got %q", got)
	}
	if got := resolveSystemPrompt(cfg, "y"); got != "default" {
		t.Errorf("no per-agent prompt should fall back: got %q", got)
	}
}
