package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

// hostsvcFixture wires a HostService against fresh ChatStore /
// SessionStore + a ReadHandler bound to a config we control.
// The chatHandler argument is optional — pass nil for tests that don't
// exercise RunSubagent / GetChatHistory.
func hostsvcFixture(t *testing.T, runtimeJSON string, withChat bool) (*HostService, talonpath.Paths, *ChatStore, *SessionStore) {
	t.Helper()
	dir := t.TempDir()
	talonDir := filepath.Join(dir, "talon")
	if err := os.MkdirAll(talonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(talonDir, "config.toml")
	if runtimeJSON != "" {
		cfg, err := talonconfig.FromRuntimeJSON([]byte(runtimeJSON))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := talonpath.Paths{
		Talon: talonpath.Layer{Dir: talonDir, Config: cfgPath},
	}
	chatStore := NewChatStore()
	sessionStore := NewSessionStore()
	reads := NewReadHandler(paths)

	var chat *ChatHandler
	if withChat {
		chat = NewChatHandler(
			&stubResolver{models: map[string]provider.ModelID{"main": "scripted/m"}},
			&stubFactory{provider: provider.NewStub("scripted", []provider.Delta{
				{Kind: provider.DeltaText, Text: "subagent reply"},
			})},
			chatStore,
		).WithSessions(sessionStore)
	}
	hs := NewHostService(paths, reads, chat, chatStore, sessionStore)
	return hs, paths, chatStore, sessionStore
}

// --- GetConfig --------------------------------------------------------

func TestHostService_GetConfig_FullEnvelope(t *testing.T) {
	hs, _, _, _ := hostsvcFixture(t, `{
		"gateway": {"port": 18789, "auth": {"token": "secret-xyz"}}
	}`, false)
	res, err := hs.GetConfig(context.Background(), &pb.GetConfigRequest{})
	if err != nil {
		t.Fatal(err)
	}
	raw := res.GetRawJson()
	if !json.Valid(raw) {
		t.Fatalf("raw is not valid JSON: %s", raw)
	}
	// Secret must be redacted (mirrors WS config.get behavior).
	if strings.Contains(string(raw), "secret-xyz") {
		t.Errorf("plugin GetConfig leaked a secret: %s", raw)
	}
	if !strings.Contains(string(raw), "REDACTED") {
		t.Errorf("expected redaction marker: %s", raw)
	}
}

func TestHostService_GetConfig_PathScoped(t *testing.T) {
	hs, _, _, _ := hostsvcFixture(t, `{
		"channels": {"telegram": {"enabled": true, "botToken": "bot-secret"}}
	}`, false)
	res, err := hs.GetConfig(context.Background(), &pb.GetConfigRequest{Path: "channels.telegram"})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(res.GetRawJson()) {
		t.Fatalf("invalid scoped JSON: %s", res.GetRawJson())
	}
	if !strings.Contains(string(res.GetRawJson()), "enabled") {
		t.Errorf("scoped result should include path content: %s", res.GetRawJson())
	}
	// Even scoped, secrets stay redacted.
	if strings.Contains(string(res.GetRawJson()), "bot-secret") {
		t.Errorf("scoped lookup leaked secret: %s", res.GetRawJson())
	}
}

func TestHostService_GetConfig_MissingPathReturnsNull(t *testing.T) {
	hs, _, _, _ := hostsvcFixture(t, `{"a":1}`, false)
	res, err := hs.GetConfig(context.Background(), &pb.GetConfigRequest{Path: "nope.does.not.exist"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.GetRawJson()) != "null" {
		t.Errorf("missing path should yield JSON null, got %s", res.GetRawJson())
	}
}

// --- ListAgents -------------------------------------------------------

func TestHostService_ListAgents_ReturnsEnvelope(t *testing.T) {
	hs, paths, _, _ := hostsvcFixture(t, `{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini", "fallbacks": []}},
			"list": [{"id": "main"}]
		}
	}`, false)
	subagentsDir := paths.Talon.SubagentsDir()
	if err := os.MkdirAll(subagentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "coding.md"), []byte(`---
description: Handles focused code changes.
model: anthropic/claude-opus-4-7
tools: [read, grep]
---
You are a focused coding subagent.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := hs.ListAgents(context.Background(), &pb.ListAgentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	raw := res.GetRawJson()
	if gjson.GetBytes(raw, "agents.#").Int() != 2 {
		t.Errorf("expected 2 agents in envelope: %s", raw)
	}
	if gjson.GetBytes(raw, `agents.#(id=="coding").kind`).Str != "subagent" {
		t.Errorf("expected coding subagent in envelope: %s", raw)
	}
	if gjson.GetBytes(raw, "defaultId").Str != "main" {
		t.Errorf("defaultId wrong: %s", raw)
	}
}

// --- GetAgentIdentity -------------------------------------------------

func TestHostService_GetAgentIdentity_ReadsIdentityMd(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Clawdia\n- **Emoji:** 🦞\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"agents":{"list":[{"id":"main","workspace":"` + wsDir + `"}]}}`
	hs, _, _, _ := hostsvcFixture(t, body, false)
	res, err := hs.GetAgentIdentity(context.Background(), &pb.GetAgentIdentityRequest{AgentId: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetName() != "Clawdia" || res.GetEmoji() != "🦞" {
		t.Errorf("identity = %+v", res)
	}
}

// --- ListModels -------------------------------------------------------

func TestHostService_ListModels_ReturnsCatalog(t *testing.T) {
	hs, _, _, _ := hostsvcFixture(t, `{
		"models": {"providers": {
			"openai": {"models": [{"id": "gpt-5.4-mini", "name": "GPT", "contextWindow": 400000, "input": ["text"], "reasoning": true}]}
		}}
	}`, false)
	res, err := hs.ListModels(context.Background(), &pb.ListModelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Verify the user-supplied model is present (not necessarily
	// first; the response now unions a built-in catalog with the
	// user's models.providers entries and sorts alphabetically).
	raw := res.GetRawJson()
	found := false
	gjson.GetBytes(raw, "models").ForEach(func(_, m gjson.Result) bool {
		if m.Get("id").Str == "gpt-5.4-mini" {
			found = true
			return false
		}
		return true
	})
	if !found {
		t.Errorf("user-defined gpt-5.4-mini missing from ListModels: %s", raw)
	}
}

// --- GetChatHistory ---------------------------------------------------

func TestHostService_GetChatHistory_ReturnsStoredMessages(t *testing.T) {
	hs, _, store, _ := hostsvcFixture(t, `{}`, true)
	store.Append("agent:main:main", "user", "hello")
	store.Append("agent:main:main", "assistant", "world")

	res, err := hs.GetChatHistory(context.Background(), &pb.GetChatHistoryRequest{
		SessionKey: "agent:main:main",
		Limit:      50,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := res.GetRawJson()
	if gjson.GetBytes(raw, "messages.#").Int() != 2 {
		t.Errorf("expected 2 messages: %s", raw)
	}
	if gjson.GetBytes(raw, "messages.0.role").Str != "user" {
		t.Errorf("first role: %s", raw)
	}
}

// --- AppendMemory -----------------------------------------------------

func TestHostService_AppendMemory_WritesToAgentWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"agents":{"list":[{"id":"main","workspace":"` + wsDir + `"}]}}`
	hs, _, _, _ := hostsvcFixture(t, body, false)
	res, err := hs.AppendMemory(context.Background(), &pb.AppendMemoryRequest{
		AgentId: "main",
		Text:    "remember this fact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetOk() {
		t.Errorf("expected ok=true")
	}
	matches, _ := filepath.Glob(filepath.Join(wsDir, "memory", "*.md"))
	if len(matches) == 0 {
		t.Fatalf("no memory file written")
	}
	body2, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(body2), "remember this fact") {
		t.Errorf("note not persisted: %s", body2)
	}
}

func TestHostService_AppendMemory_NoWorkspaceFailsClearly(t *testing.T) {
	hs, _, _, _ := hostsvcFixture(t, `{"agents":{"list":[{"id":"main"}]}}`, false)
	_, err := hs.AppendMemory(context.Background(), &pb.AppendMemoryRequest{
		AgentId: "main",
		Text:    "x",
	})
	if err == nil {
		t.Fatal("expected error when no workspace resolves")
	}
}

// --- RunSubagent ------------------------------------------------------

func TestHostService_RunSubagent_DelegatesToChatHandler(t *testing.T) {
	hs, _, _, _ := hostsvcFixture(t, `{}`, true)
	res, err := hs.RunSubagent(context.Background(), &pb.RunSubagentRequest{
		AgentId: "main",
		Prompt:  "do something",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stub provider's scripted reply was "subagent reply".
	if res.GetText() != "subagent reply" {
		t.Errorf("got %q, want %q", res.GetText(), "subagent reply")
	}
}

func TestHostService_RunSubagent_NoChatReturnsUnimplemented(t *testing.T) {
	hs, _, _, _ := hostsvcFixture(t, `{}`, false)
	_, err := hs.RunSubagent(context.Background(), &pb.RunSubagentRequest{AgentId: "main", Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "chat not configured") {
		t.Errorf("expected Unimplemented chat-not-configured, got %v", err)
	}
}

// --- ListSessions -----------------------------------------------------

func TestHostService_ListSessions_IncludesPatchedAndChatOnly(t *testing.T) {
	hs, _, store, sessions := hostsvcFixture(t, `{}`, false)
	sessions.Patch("agent:main:main", map[string]json.RawMessage{
		"model": json.RawMessage(`"openai/gpt-4o-mini"`),
	})
	store.Append("agent:coding:main", "user", "code question")

	res, err := hs.ListSessions(context.Background(), &pb.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	raw := res.GetRawJson()
	if gjson.GetBytes(raw, "sessions.#").Int() != 2 {
		t.Errorf("expected 2 sessions (patched + chat-only): %s", raw)
	}
}
