package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/openclaw"
)

// readFixture builds a Paths backed by a temp dir and writes the openclaw
// layer's openclaw.json. The talon overlay starts empty.
func readFixture(t *testing.T, openclawJSON string) openclaw.Paths {
	t.Helper()
	dir := t.TempDir()
	talonDir := filepath.Join(dir, "talon")
	openclawDir := filepath.Join(dir, "openclaw")
	if err := os.MkdirAll(talonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(openclawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	openclawCfg := filepath.Join(openclawDir, "openclaw.json")
	if openclawJSON != "" {
		if err := os.WriteFile(openclawCfg, []byte(openclawJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return openclaw.Paths{
		Talon:    openclaw.Layer{Dir: talonDir, Config: filepath.Join(talonDir, "openclaw.json")},
		Openclaw: openclaw.Layer{Dir: openclawDir, Config: openclawCfg},
	}
}

const fixtureRealConfig = `{
	"agents": {
		"defaults": {
			"model": {
				"primary": "openai/gpt-5.4-mini",
				"fallbacks": ["anthropic/claude-opus-4-7", "deepseek/deepseek-reasoner"]
			},
			"models": {
				"openai/gpt-5.4-mini": {"alias": "mini"},
				"deepseek/deepseek-reasoner": {"alias": "deepseek"},
				"anthropic/claude-opus-4-7": {}
			}
		},
		"list": [
			{"id": "main", "tools": {"profile": "full"}},
			{"id": "coding", "model": "anthropic/claude-opus-4-7", "name": "coding", "workspace": "/ws/coding"},
			{"id": "research", "model": {"primary": "anthropic/claude-sonnet-4-6"}, "name": "research"}
		]
	},
	"models": {
		"providers": {
			"openai": {
				"models": [
					{"id": "gpt-5.4-mini", "name": "GPT-5.4 mini", "contextWindow": 400000, "input": ["text", "image"], "reasoning": true}
				]
			},
			"anthropic": {
				"models": [
					{"id": "claude-opus-4-7", "name": "Claude Opus 4.7", "contextWindow": 1000000, "input": ["text", "image"], "reasoning": true}
				]
			},
			"deepseek": {
				"models": [
					{"id": "deepseek-reasoner", "name": "DeepSeek Reasoner", "contextWindow": 131072, "input": ["text"], "reasoning": true}
				]
			}
		}
	}
}`

func TestAgentsList_ResolvesEachAgentModel(t *testing.T) {
	h := NewReadHandler(readFixture(t, fixtureRealConfig))
	res, ferr := h.handleAgentsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	env := res.(map[string]any)
	agents := env["agents"].([]map[string]any)
	if len(agents) != 3 {
		t.Fatalf("got %d agents, want 3", len(agents))
	}
	byID := make(map[string]map[string]any, 3)
	for _, a := range agents {
		byID[a["id"].(string)] = a
	}
	// "main" inherits primary from defaults (no per-agent model field).
	if got := byID["main"]["model"].(map[string]any)["primary"]; got != "openai/gpt-5.4-mini" {
		t.Errorf("main.model.primary = %v, want openai/gpt-5.4-mini (defaults)", got)
	}
	// "coding" uses per-agent string shorthand.
	if got := byID["coding"]["model"].(map[string]any)["primary"]; got != "anthropic/claude-opus-4-7" {
		t.Errorf("coding.model.primary = %v, want anthropic/claude-opus-4-7", got)
	}
	// "research" uses per-agent object form.
	if got := byID["research"]["model"].(map[string]any)["primary"]; got != "anthropic/claude-sonnet-4-6" {
		t.Errorf("research.model.primary = %v, want anthropic/claude-sonnet-4-6", got)
	}
	// All agents share the defaults' fallbacks list.
	for _, id := range []string{"main", "coding", "research"} {
		fb := byID[id]["model"].(map[string]any)["fallbacks"].([]string)
		if len(fb) != 2 {
			t.Errorf("%s: fallbacks len = %d, want 2", id, len(fb))
		}
	}
	// Envelope shape.
	if env["defaultId"] != "main" || env["mainKey"] != "main" || env["scope"] != "per-sender" {
		t.Errorf("envelope = %+v", env)
	}
}

func TestAgentsList_DefaultIDFallsBackToFirstAgentWhenNoMain(t *testing.T) {
	body := `{"agents":{"list":[{"id":"alpha"},{"id":"beta"}]}}`
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleAgentsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["defaultId"] != "alpha" {
		t.Errorf("defaultId = %v, want first agent 'alpha'", res.(map[string]any)["defaultId"])
	}
}

func TestModelsList_FlattensProvidersAndAttachesAliases(t *testing.T) {
	h := NewReadHandler(readFixture(t, fixtureRealConfig))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	byID := make(map[string]map[string]any, 3)
	for _, m := range models {
		byID[m["id"].(string)] = m
	}
	// gpt-5.4-mini has alias "mini"
	if alias, ok := byID["gpt-5.4-mini"]["alias"]; !ok || alias != "mini" {
		t.Errorf("gpt-5.4-mini alias = %v (ok=%v), want 'mini'", alias, ok)
	}
	// deepseek-reasoner has alias "deepseek"
	if alias := byID["deepseek-reasoner"]["alias"]; alias != "deepseek" {
		t.Errorf("deepseek-reasoner alias = %v", alias)
	}
	// claude-opus-4-7 has no alias entry — alias key should be absent.
	if _, ok := byID["claude-opus-4-7"]["alias"]; ok {
		t.Errorf("claude-opus-4-7 should not have alias: %+v", byID["claude-opus-4-7"])
	}
	// Provider/contextWindow/input/reasoning roundtrip.
	gpt := byID["gpt-5.4-mini"]
	if gpt["provider"] != "openai" || gpt["contextWindow"].(int64) != 400000 || gpt["reasoning"] != true {
		t.Errorf("gpt-5.4-mini fields wrong: %+v", gpt)
	}
	inputs := gpt["input"].([]string)
	if len(inputs) != 2 || inputs[0] != "text" || inputs[1] != "image" {
		t.Errorf("gpt-5.4-mini inputs = %+v", inputs)
	}
	// Sorted by id.
	prev := ""
	for _, m := range models {
		id := m["id"].(string)
		if prev != "" && id < prev {
			t.Errorf("models not sorted by id: %s before %s", prev, id)
		}
		prev = id
	}
}

func TestModelsList_EmptyWhenNoProvidersConfigured(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{}`))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	if len(models) != 0 {
		t.Errorf("empty config should yield 0 models, got %d", len(models))
	}
}

func TestConfigSchema_ReturnsCachedEnvelope(t *testing.T) {
	paths := readFixture(t, `{}`)
	if err := os.MkdirAll(paths.Talon.CacheDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cache := []byte(`{"generatedAt":"2026-04-27T00:00:00Z","schema":{"type":"object"}}`)
	if err := os.WriteFile(paths.Talon.SchemaCachePath(), cache, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewReadHandler(paths)
	res, ferr := h.handleConfigSchema(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	got, ok := res.(json.RawMessage)
	if !ok {
		t.Fatalf("config.schema returned %T, want json.RawMessage", res)
	}
	if string(got) != string(cache) {
		t.Errorf("config.schema response not byte-identical to cache:\ngot:  %s\nwant: %s", got, cache)
	}
}

func TestConfigSchema_MissingCacheReturnsHelpfulError(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{}`))
	_, ferr := h.handleConfigSchema(t.Context(), HandlerCtx{}, nil)
	if ferr == nil {
		t.Fatal("expected error when cache is missing")
	}
	if !strings.Contains(ferr.Message, "schema --refresh") {
		t.Errorf("error should hint at the refresh command: %q", ferr.Message)
	}
}

func TestConfigSchema_InvalidCacheReturnsError(t *testing.T) {
	paths := readFixture(t, `{}`)
	if err := os.MkdirAll(paths.Talon.CacheDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Talon.SchemaCachePath(), []byte("garbage{"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewReadHandler(paths)
	_, ferr := h.handleConfigSchema(t.Context(), HandlerCtx{}, nil)
	if ferr == nil || !strings.Contains(ferr.Message, "valid JSON") {
		t.Errorf("expected invalid-JSON error, got %+v", ferr)
	}
}

func TestReadHandler_RegisterAddsAll(t *testing.T) {
	r := NewRegistry()
	NewReadHandler(readFixture(t, `{}`)).Register(r)
	want := map[string]bool{"agents.list": false, "models.list": false, "config.schema": false, "agent.identity.get": false, "skills.status": false}
	for _, m := range r.Methods() {
		if _, ok := want[m]; ok {
			want[m] = true
		}
	}
	for m, ok := range want {
		if !ok {
			t.Errorf("Register did not register %q", m)
		}
	}
}

// --- agent.identity.get ----------------------------------------------------

func TestParseIdentityMarkdown_RealFormat(t *testing.T) {
	// Verbatim from openclaw's IDENTITY.md template + bootstrap.
	body := []byte(`# IDENTITY.md - Who Am I?

_Fill this in during your first conversation. Make it yours._

- **Name:** Clawdia
- **Creature:** personal assistant / automation lobster
- **Vibe:** gets shit done, cute, butter-light tan, practical
- **Emoji:** 🦞
- **Avatar:**
  _(workspace-relative path, http(s) URL, or data URI)_

---

This isn't just metadata.`)
	got := parseIdentityMarkdown(body)
	if got.name != "Clawdia" {
		t.Errorf("name = %q, want Clawdia", got.name)
	}
	if got.emoji != "🦞" {
		t.Errorf("emoji = %q, want 🦞", got.emoji)
	}
	// Avatar is empty in the template; the italic continuation hint must
	// NOT be treated as the value.
	if got.avatar != "" {
		t.Errorf("avatar = %q, want empty (italic hint should be ignored)", got.avatar)
	}
}

func TestParseIdentityMarkdown_FilledAvatar(t *testing.T) {
	body := []byte("- **Name:** Bot\n- **Emoji:** 🤖\n- **Avatar:** avatars/me.png\n")
	got := parseIdentityMarkdown(body)
	if got.name != "Bot" || got.emoji != "🤖" || got.avatar != "avatars/me.png" {
		t.Errorf("got %+v", got)
	}
}

func TestParseIdentityMarkdown_EmptyDocument(t *testing.T) {
	got := parseIdentityMarkdown([]byte(""))
	if got.name != "" || got.emoji != "" || got.avatar != "" {
		t.Errorf("expected zero value: %+v", got)
	}
}

func TestParseIdentityMarkdown_TolerantOfWhitespaceAndCase(t *testing.T) {
	body := []byte("   - **name:**   Lower  \n- **EMOJI:**🦞\n")
	got := parseIdentityMarkdown(body)
	if got.name != "Lower" {
		t.Errorf("name = %q, want Lower (lowercase key)", got.name)
	}
	if got.emoji != "🦞" {
		t.Errorf("emoji = %q, want 🦞 (uppercase key)", got.emoji)
	}
}

func TestAgentIdentityGet_HappyPath(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Clawdia\n- **Emoji:** 🦞\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main"}`))
	if ferr != nil {
		t.Fatalf("handleAgentIdentityGet: %+v", ferr)
	}
	m := res.(map[string]any)
	if m["agentId"] != "main" || m["name"] != "Clawdia" || m["emoji"] != "🦞" {
		t.Errorf("identity = %+v", m)
	}
	// avatar falls back to emoji when the field is empty.
	if m["avatar"] != "🦞" {
		t.Errorf("avatar should fall back to emoji, got %q", m["avatar"])
	}
}

func TestAgentIdentityGet_FallsBackToDefaultsWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "default-ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Defaulted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{
		"agents": {
			"defaults": {"workspace": %q},
			"list": [{"id": "main"}]
		}
	}`, wsDir)
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["name"] != "Defaulted" {
		t.Errorf("name = %v, want Defaulted (resolved via agents.defaults.workspace)", res)
	}
}

func TestAgentIdentityGet_UnknownAgentReturnsNull(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{"agents":{"list":[{"id":"main"}]}}`))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{"agentId":"nope"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res != nil {
		t.Errorf("expected nil for unknown agent, got %+v", res)
	}
}

func TestAgentIdentityGet_MissingIdentityFileReturnsNull(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res != nil {
		t.Errorf("expected nil when IDENTITY.md is absent, got %+v", res)
	}
}

// --- skills.status ---------------------------------------------------------

func TestSkillsStatus_ReturnsEmptyEnvelopeWithResolvedWorkspace(t *testing.T) {
	body := `{
		"agents": {
			"defaults": {"workspace": "/ws/default"},
			"list": [{"id": "main", "workspace": "/ws/main"}]
		}
	}`
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleSkillsStatus(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	m := res.(map[string]any)
	if m["workspaceDir"] != "/ws/main" {
		t.Errorf("workspaceDir = %v, want /ws/main", m["workspaceDir"])
	}
	if m["managedSkillsDir"] != "/ws/main/.skills" {
		t.Errorf("managedSkillsDir = %v", m["managedSkillsDir"])
	}
	skills, ok := m["skills"].([]any)
	if !ok || len(skills) != 0 {
		t.Errorf("skills = %v, want empty []any", m["skills"])
	}
}

func TestSkillsStatus_FallsBackToDefaultsWorkspace(t *testing.T) {
	body := `{
		"agents": {
			"defaults": {"workspace": "/ws/default"},
			"list": [{"id": "main"}]
		}
	}`
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleSkillsStatus(t.Context(), HandlerCtx{}, []byte(`{}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["workspaceDir"] != "/ws/default" {
		t.Errorf("workspaceDir should fall back to defaults: %+v", res)
	}
}

func TestSkillsStatus_NoParamsDefaultsToMainAgent(t *testing.T) {
	body := `{"agents":{"list":[{"id":"main","workspace":"/ws/m"}]}}`
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleSkillsStatus(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["workspaceDir"] != "/ws/m" {
		t.Errorf("default agent should be main: %+v", res)
	}
}

func TestAgentIdentityGet_NoParamsDefaultsToMain(t *testing.T) {
	// The openclaw web UI sometimes calls with {} (no sessionKey, no
	// agentId). Returning BAD_REQUEST silently breaks the chat label
	// because the UI swallows the error. Default to "main" instead.
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Clawdia\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{}`))
	if ferr != nil {
		t.Fatalf("expected to default to main, got error: %+v", ferr)
	}
	if res.(map[string]any)["name"] != "Clawdia" {
		t.Errorf("expected default-main resolution: %+v", res)
	}
}

func TestAgentIdentityGet_AcceptsSessionKey(t *testing.T) {
	// The UI's controllers/assistant-identity.ts calls
	// `client.request("agent.identity.get", {sessionKey})` — never
	// agentId. Server must derive agentId from sessionKey.
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Clawdia\n- **Emoji:** 🦞\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))

	// All three canonical sessionKey forms must resolve to "main".
	for _, sk := range []string{"main", "agent:main", "agent:main:main"} {
		params := fmt.Sprintf(`{"sessionKey":%q}`, sk)
		res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(params))
		if ferr != nil {
			t.Errorf("sessionKey=%q got error %+v", sk, ferr)
			continue
		}
		m, ok := res.(map[string]any)
		if !ok || m["name"] != "Clawdia" || m["agentId"] != "main" {
			t.Errorf("sessionKey=%q returned %+v", sk, res)
		}
	}
}

func TestAgentIdentityGet_ExplicitAgentIDWinsOverSessionKey(t *testing.T) {
	// When both are provided, explicit agentId beats the derived one.
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Coder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main"},{"id":"coding","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{"agentId":"coding","sessionKey":"agent:main:main"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["name"] != "Coder" {
		t.Errorf("expected agentId=coding to win, got %+v", res)
	}
}
