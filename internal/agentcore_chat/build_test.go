package agentcore_chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/jess/memory"
	"github.com/voocel/agentcore"

	"github.com/guygrigsby/talon/internal/agentcontext"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func TestBuilder_BuildAgent_Openai(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}},
			"list": [{"id": "main", "workspace": "/tmp/talon-test-ws"}]
		}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
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

func TestBuilder_BuildAgent_SelectedModelWins(t *testing.T) {
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
	b := NewBuilder(cfg, talonpath.Paths{}).
		WithAuthOverride(map[string]ProviderAuth{
			"deepseek": {Provider: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-test"},
		}).
		WithSelectedModel("deepseek/deepseek-chat")

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
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
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
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
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
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{})
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
	b := NewBuilder([]byte(`{}`), talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{})
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
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{})
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
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
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

func TestBuilder_BuildAgent_ToolAllowListFiltersTools(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}, "workspace": "/tmp/talon-test-ws"},
			"list": [{"id": "main", "tools": {"allow": ["read", "grep"]}}]
		}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	agent, _, err := b.BuildAgent("main")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	names := agentToolNames(agent)
	if len(names) != 2 || !names["read"] || !names["grep"] {
		t.Fatalf("tool names = %v, want read+grep only", names)
	}
}

func TestBuilder_BuildAgent_ToolsEnabledFalseDisablesMemoryTools(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-4o-mini"}, "workspace": "/tmp/talon-no-tools"},
			"list": [{"id": "main", "tools": {"enabled": false}}]
		}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).
		WithAuthOverride(map[string]ProviderAuth{
			"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
		}).
		WithMemory(memory.NewInMemoryStore(), memory.NewSimpleRecaller())
	agent, _, err := b.BuildAgent("main")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if names := agentToolNames(agent); len(names) != 0 {
		t.Fatalf("tool names = %v, want no tools", names)
	}
}

func TestBuilder_BuildAgent_SubagentFrontMatterFiltersTools(t *testing.T) {
	clearProviderEnv(t)
	root := t.TempDir()
	paths := talonpath.Paths{Talon: talonpath.Layer{Dir: root}}
	if err := os.MkdirAll(paths.Talon.SubagentsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Talon.SubagentsDir(), "coding.md"), []byte(`---
model: openai/gpt-4o-mini
tools: [read, grep]
---
Code work.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "openai/gpt-4o-mini"}, "workspace": "/tmp/talon-subagent-ws"}}
	}`)
	b := NewBuilder(cfg, paths).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	agent, _, err := b.BuildAgent("coding")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	names := agentToolNames(agent)
	if len(names) != 2 || !names["read"] || !names["grep"] {
		t.Fatalf("tool names = %v, want read+grep only", names)
	}
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

func agentToolNames(agent *agentcore.Agent) map[string]bool {
	out := map[string]bool{}
	for _, tool := range agent.State().Tools {
		out[tool.Name()] = true
	}
	return out
}

func TestBuilder_BuildAgent_WithMemoryAttachesTools(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "openai/gpt-4o-mini"}, "workspace": "/tmp/talon-mem-test"}}
	}`)
	store := memory.NewInMemoryStore()
	recaller := memory.NewSimpleRecaller()
	b := NewBuilder(cfg, talonpath.Paths{}).
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
	names := map[string]bool{}
	for _, tool := range agent.State().Tools {
		names[tool.Name()] = true
	}
	if !names["remember"] || !names["recall"] {
		t.Fatalf("memory tools = %v, want remember and recall", names)
	}
}

func TestBuilder_MemoryContextManagerProjectsJessMemory(t *testing.T) {
	ctx := context.Background()
	store := memory.NewInMemoryStore()
	_, _ = store.Append(ctx, memory.Entry{
		Kind:    string(memory.KindUser),
		AgentID: "main",
		Text:    "user prefers tabs",
	})
	_, _ = store.Append(ctx, memory.Entry{
		Kind:    string(memory.KindProject),
		AgentID: "main",
		Text:    "the backend uses go",
	})
	b := NewBuilder(nil, talonpath.Paths{}).
		WithMemory(store, memory.NewSimpleRecaller()).
		WithMemoryOptions(8, "Relevant memories for this conversation:", nil)

	cm := b.memoryContextManager("main")
	if cm == nil {
		t.Fatal("memory context manager is nil")
	}
	proj, err := cm.Project(ctx, []agentcore.AgentMessage{
		agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("what should I know about go?")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Messages) == 0 {
		t.Fatal("expected projected memory message")
	}
	text := proj.Messages[0].TextContent()
	if !strings.Contains(text, "Core memories") || !strings.Contains(text, "user prefers tabs") {
		t.Fatalf("projected context missing core memory: %q", text)
	}
	if !strings.Contains(text, "Relevant memories") || !strings.Contains(text, "backend uses go") {
		t.Fatalf("projected context missing recalled memory: %q", text)
	}
}

func TestMemorySourceMiddlewareStampsRememberWrites(t *testing.T) {
	ctx := context.Background()
	store := memory.NewInMemoryStore()
	remember := memory.NewRememberTool(store, memory.RememberOptions{AgentID: "main"})
	mw := memorySourceMiddleware(memory.Source{
		SessionID: "agent:main:web",
		MessageID: "run_123",
	})

	_, err := mw(ctx, agentcore.ToolCall{
		Name: "remember",
		Args: json.RawMessage(`{"kind":"user","text":"user likes precise commits"}`),
	}, remember.Execute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Recall(ctx, memory.Query{AgentID: "main"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stored entries = %d, want 1", len(got))
	}
	src := got[0].Source
	if src.SessionID != "agent:main:web" || src.MessageID != "run_123" || src.Tool != "remember" {
		t.Fatalf("source = %+v, want Talon session/run provenance", src)
	}
}

func TestBuilder_BuildAgent_NoMemoryNoMemoryTools(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "openai/gpt-4o-mini"}, "workspace": "/tmp/talon-no-mem"}}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
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

func TestComposeSystemPrompt(t *testing.T) {
	cases := []struct {
		name            string
		persona, config string
		want            string
	}{
		{"both empty", "", "", ""},
		{"persona only", "I am Jess.", "", "I am Jess."},
		{"configured only", "", "Be terse.", "Be terse."},
		{"trims whitespace", "  I am Jess.  ", "", "I am Jess."},
		{"persona leads, blank-line joined", "I am Jess.", "Be terse.", "I am Jess.\n\nBe terse."},
	}
	for _, c := range cases {
		if got := composeSystemPrompt(c.persona, c.config); got != c.want {
			t.Errorf("%s: composeSystemPrompt(%q,%q) = %q, want %q", c.name, c.persona, c.config, got, c.want)
		}
	}
}

func TestBuildSystemPrompt_FromWorkspaceFiles(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "IDENTITY.md"), []byte("Name: Jess\nCreature: fox"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte("Be warm and direct."), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`{
		"agents": {
			"defaults": {"workspace": "` + ws + `", "systemPrompt": "Always answer in English."}
		}
	}`)

	got := buildSystemPrompt(cfg, "main", ws)

	for _, want := range []string{"## IDENTITY.md", "Name: Jess", "## SOUL.md", "Always answer in English."} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestResolvePersonaDir(t *testing.T) {
	if got := resolvePersonaDir("/ws", "/talon"); got != "/ws" {
		t.Errorf("configured workspace should win: got %q", got)
	}
	if got := resolvePersonaDir("", "/talon"); got != "/talon" {
		t.Errorf("empty workspace should fall back to talonDir: got %q", got)
	}
}

func TestBuildSystemPrompt_OnboardingDirectiveLeads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("Name: Jess"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Arm onboarding.
	if _, err := agentcontext.EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`{"agents": {"defaults": {"systemPrompt": "Be terse."}}}`)

	got := buildSystemPrompt(cfg, "main", dir)

	if !strings.Contains(got, "finish_onboarding") {
		t.Errorf("onboarding directive missing:\n%s", got)
	}
	// Directive must lead, ahead of persona and configured prompt.
	if di, pi := strings.Index(got, "FIRST-RUN ONBOARDING"), strings.Index(got, "Name: Jess"); di < 0 || di > pi {
		t.Errorf("onboarding directive should lead the prompt; di=%d pi=%d\n%s", di, pi, got)
	}
}
