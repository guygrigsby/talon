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
	// Models is now: built-in catalog (openai + deepseek defaults) ∪
	// user config (3 entries). Catalog has no anthropic, so the 3
	// user entries (gpt-5.4-mini, claude-opus-4-7, deepseek-reasoner)
	// each appear once; gpt-5.4-mini and deepseek-reasoner OVERRIDE
	// the catalog's own entries (collision keys), claude-opus-4-7
	// adds a row.
	byID := make(map[string]map[string]any, len(models))
	for _, m := range models {
		byID[m["id"].(string)] = m
	}
	// User-defined entries still surface with their fields:
	if alias, ok := byID["gpt-5.4-mini"]["alias"]; !ok || alias != "mini" {
		t.Errorf("gpt-5.4-mini alias = %v (ok=%v), want 'mini'", alias, ok)
	}
	if alias := byID["deepseek-reasoner"]["alias"]; alias != "deepseek" {
		t.Errorf("deepseek-reasoner alias = %v", alias)
	}
	if _, ok := byID["claude-opus-4-7"]["alias"]; ok {
		t.Errorf("claude-opus-4-7 should not have alias: %+v", byID["claude-opus-4-7"])
	}
	// User config wins on collision: gpt-5.4-mini's contextWindow
	// must be the user-supplied 400000, not whatever the catalog had
	// (catalog doesn't ship gpt-5.4-mini, but the principle holds).
	gpt := byID["gpt-5.4-mini"]
	if gpt["provider"] != "openai" || gpt["contextWindow"].(int64) != 400000 || gpt["reasoning"] != true {
		t.Errorf("gpt-5.4-mini fields wrong: %+v", gpt)
	}
	inputs := gpt["input"].([]string)
	if len(inputs) != 2 || inputs[0] != "text" || inputs[1] != "image" {
		t.Errorf("gpt-5.4-mini inputs = %+v", inputs)
	}

	// Built-in OpenAI catalog should also be present.
	if _, ok := byID["gpt-4o"]; !ok {
		t.Errorf("expected built-in gpt-4o entry; got ids = %v", keysOf(byID))
	}
}

func TestModelsList_EmptyConfigStillReturnsBuiltInCatalog(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{}`))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	if len(models) == 0 {
		t.Fatal("empty config should still surface the built-in model catalog")
	}
	// Spot-check: the catalog must include common production models so
	// fresh installs aren't stuck with "no models in picker."
	want := []string{"gpt-4o", "gpt-4o-mini", "o1", "o3-mini", "deepseek-chat"}
	gotIDs := map[string]bool{}
	for _, m := range models {
		gotIDs[m["id"].(string)] = true
	}
	for _, id := range want {
		if !gotIDs[id] {
			t.Errorf("built-in catalog missing %q; got %v", id, keysOfBool(gotIDs))
		}
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
	want := map[string]bool{
		"agents.list":        false,
		"models.list":        false,
		"config.get":         false,
		"config.schema":      false,
		"agent.identity.get": false,
		"skills.status":      false,
		"memory.append":      false,
	}
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

// --- config.get -----------------------------------------------------------

func TestConfigGet_EnvelopeShapeAndHash(t *testing.T) {
	body := `{"gateway":{"port":18789}}`
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	m := res.(map[string]any)
	for _, k := range []string{"path", "exists", "raw", "hash", "parsed", "valid", "config", "issues"} {
		if _, ok := m[k]; !ok {
			t.Errorf("envelope missing %q: %+v", k, m)
		}
	}
	if !m["valid"].(bool) {
		t.Errorf("valid should be true for parseable config")
	}
	// hash should be a 64-char hex sha256
	if h := m["hash"].(string); len(h) != 64 {
		t.Errorf("hash length = %d, want 64", len(h))
	}
	// raw should be valid JSON.
	if !json.Valid([]byte(m["raw"].(string))) {
		t.Errorf("raw is not valid JSON: %s", m["raw"])
	}
}

func TestConfigGet_RedactsKnownSecretLeaves(t *testing.T) {
	body := `{
		"gateway": {
			"port": 18789,
			"auth": {
				"mode": "token",
				"token": "real-secret-token",
				"password": "real-password"
			}
		},
		"channels": {
			"telegram": {"enabled": true, "botToken": "telegram-secret"},
			"slack": {"token": "slack-secret"}
		},
		"plugins": {"entries": {"brave": {"config": {"webSearch": {"apiKey": "brave-secret"}}}}},
		"skills": {"entries": {"openai-whisper-api": {"apiKey": "whisper-secret"}}}
	}`
	h := NewReadHandler(readFixture(t, body))
	res, _ := h.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	rawBytes := []byte(res.(map[string]any)["raw"].(string))

	for _, secret := range []string{
		"real-secret-token", "real-password", "telegram-secret", "slack-secret", "brave-secret", "whisper-secret",
	} {
		if strings.Contains(string(rawBytes), secret) {
			t.Errorf("config.get response leaked secret %q", secret)
		}
	}
	if !strings.Contains(string(rawBytes), "***REDACTED***") {
		t.Errorf("expected ***REDACTED*** marker in output")
	}
	// Non-secret values must survive.
	if !strings.Contains(string(rawBytes), `"port": 18789`) {
		t.Errorf("non-secret port lost in redaction:\n%s", rawBytes)
	}
	if !strings.Contains(string(rawBytes), `"mode": "token"`) {
		t.Errorf("non-secret mode lost in redaction (mode is enum, not secret):\n%s", rawBytes)
	}
}

func TestConfigGet_HashChangesWhenContentChanges(t *testing.T) {
	h1 := NewReadHandler(readFixture(t, `{"a":1}`))
	h2 := NewReadHandler(readFixture(t, `{"a":2}`))
	r1, _ := h1.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	r2, _ := h2.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	if r1.(map[string]any)["hash"] == r2.(map[string]any)["hash"] {
		t.Errorf("hash should differ for different content")
	}
}

func TestConfigGet_ExistsReflectsTalonOverlay(t *testing.T) {
	// readFixture writes the openclaw layer but never the talon overlay,
	// so exists should be false until something writes ~/.talon/openclaw.json.
	h := NewReadHandler(readFixture(t, `{"a":1}`))
	res, _ := h.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	if res.(map[string]any)["exists"].(bool) {
		t.Errorf("exists should be false when talon overlay is absent")
	}
}

// --- memory.append ---------------------------------------------------------

func TestMemoryAppend_WritesToAgentWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))

	res, ferr := h.handleMemoryAppend(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main","text":"important fact"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	if !res.(map[string]any)["ok"].(bool) {
		t.Errorf("ok: %+v", res)
	}
	matches, _ := filepath.Glob(filepath.Join(wsDir, "memory", "*.md"))
	if len(matches) == 0 {
		t.Fatalf("no memory file written under %s", wsDir)
	}
	bodyBytes, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(bodyBytes), "important fact") {
		t.Errorf("note not persisted: %q", bodyBytes)
	}
}

func TestMemoryAppend_AcceptsSessionKey(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))

	if _, ferr := h.handleMemoryAppend(t.Context(), HandlerCtx{}, []byte(`{"sessionKey":"agent:main:main","text":"via session"}`)); ferr != nil {
		t.Fatal(ferr)
	}
	matches, _ := filepath.Glob(filepath.Join(wsDir, "memory", "*.md"))
	if len(matches) == 0 {
		t.Fatalf("session-key path didn't write: %v", matches)
	}
}

func TestMemoryAppend_RejectsEmptyText(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{"agents":{"list":[{"id":"main","workspace":"/tmp"}]}}`))
	_, ferr := h.handleMemoryAppend(t.Context(), HandlerCtx{}, []byte(`{"text":""}`))
	if ferr == nil || ferr.Code != ErrCodeBadRequest {
		t.Errorf("want BAD_REQUEST for empty text, got %+v", ferr)
	}
}

func TestMemoryAppend_AgentWithoutWorkspaceErrors(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{"agents":{"list":[{"id":"main"}]}}`))
	_, ferr := h.handleMemoryAppend(t.Context(), HandlerCtx{}, []byte(`{"text":"x"}`))
	if ferr == nil || ferr.Code != ErrCodeInternal {
		t.Errorf("want INTERNAL when no workspace resolves, got %+v", ferr)
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
