package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	plugin "github.com/guygrigsby/talon/internal/plugin/host"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// readFixture builds a Paths backed by a temp Talon state dir.
func readFixture(t *testing.T, runtimeJSON string) talonpath.Paths {
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
	return talonpath.Paths{
		Talon: talonpath.Layer{Dir: talonDir, Config: cfgPath},
	}
}

func writeSubagentFixture(t *testing.T, paths talonpath.Paths, name, body string) {
	t.Helper()
	dir := paths.Talon.SubagentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
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
					{"id": "gpt-5.4-mini", "name": "GPT-5.4 mini", "contextWindow": 400000, "maxTokens": 128000, "input": ["text", "image"], "reasoning": true, "cost": {"input": 1.25, "output": 10, "cacheRead": 0.125, "cacheWrite": 1.25}}
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
	paths := readFixture(t, fixtureRealConfig)
	writeSubagentFixture(t, paths, "coding.md", `---
name: coding
description: Code-heavy tasks
model: anthropic/claude-opus-4-7
tools: [read, grep, edit]
---
Use for code-heavy tasks.
`)
	writeSubagentFixture(t, paths, "research.md", `---
name: research
description: Research and summaries
model: anthropic/claude-sonnet-4-6
---
Use for research and summaries.
`)
	h := NewReadHandler(paths)
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
	if byID["coding"]["kind"] != "subagent" || byID["coding"]["source"] != "subagent-file" {
		t.Errorf("coding row did not report file-backed subagent: %+v", byID["coding"])
	}
	if got := byID["coding"]["tools"].([]string); len(got) != 3 || got[0] != "edit" || got[1] != "grep" || got[2] != "read" {
		t.Errorf("coding tools = %+v", got)
	}
	// Envelope shape.
	if env["defaultId"] != "main" || env["mainKey"] != "main" || env["scope"] != "per-sender" {
		t.Errorf("envelope = %+v", env)
	}
}

func TestAgentsList_DefaultIDIsMainFromNativeConfig(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{"agents":{"defaults":{"model":{"primary":"openai/gpt-4o-mini"}}}}`))
	res, ferr := h.handleAgentsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["defaultId"] != "main" {
		t.Errorf("defaultId = %v, want main", res.(map[string]any)["defaultId"])
	}
}

func TestAgentsList_AttachesResolvedToolPolicy(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-4o-mini"}},
			"list": [{"id": "main", "tools": {"allow": ["read", "grep"]}}]
		}
	}`))
	res, ferr := h.handleAgentsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	agents := res.(map[string]any)["agents"].([]map[string]any)
	if len(agents) != 1 {
		t.Fatalf("agents = %+v", agents)
	}
	tools := agents[0]["tools"].([]string)
	if len(tools) != 2 || tools[0] != "grep" || tools[1] != "read" {
		t.Fatalf("tools = %+v, want grep+read", tools)
	}
}

func TestAgentsList_AttachesDisabledToolPolicy(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-4o-mini"}},
			"list": [{"id": "main", "tools": {"enabled": false}}]
		}
	}`))
	res, ferr := h.handleAgentsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	agents := res.(map[string]any)["agents"].([]map[string]any)
	tools, ok := agents[0]["tools"].([]string)
	if !ok || len(tools) != 0 {
		t.Fatalf("tools = %#v, want explicit empty list", agents[0]["tools"])
	}
}

func TestModelsList_FlattensProvidersAndAttachesAliases(t *testing.T) {
	h := NewReadHandler(readFixture(t, fixtureRealConfig))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	// Models comes from user-config overlays only here (no plugin
	// host wired in the test, no catalog floor). Three user entries:
	// gpt-5.4-mini, claude-opus-4-7, deepseek-reasoner.
	byID := make(map[string]map[string]any, len(models))
	for _, m := range models {
		byID[m["id"].(string)] = m
	}
	if alias, ok := byID["gpt-5.4-mini"]["alias"]; !ok || alias != "mini" {
		t.Errorf("gpt-5.4-mini alias = %v (ok=%v), want 'mini'", alias, ok)
	}
	if alias := byID["deepseek-reasoner"]["alias"]; alias != "deepseek" {
		t.Errorf("deepseek-reasoner alias = %v", alias)
	}
	if _, ok := byID["claude-opus-4-7"]["alias"]; ok {
		t.Errorf("claude-opus-4-7 should not have alias: %+v", byID["claude-opus-4-7"])
	}
	gpt := byID["gpt-5.4-mini"]
	if gpt["provider"] != "openai" || gpt["contextWindow"].(int64) != 400000 || gpt["reasoning"] != true {
		t.Errorf("gpt-5.4-mini fields wrong: %+v", gpt)
	}
	if gpt["maxTokens"].(int64) != 128000 {
		t.Errorf("gpt-5.4-mini maxTokens = %v", gpt["maxTokens"])
	}
	inputs := gpt["input"].([]string)
	if len(inputs) != 2 || inputs[0] != "text" || inputs[1] != "image" {
		t.Errorf("gpt-5.4-mini inputs = %+v", inputs)
	}
	cost := gpt["cost"].(map[string]any)
	if cost["input"].(float64) != 1.25 || cost["output"].(float64) != 10.0 || cost["source"] != "catalog" {
		t.Errorf("gpt-5.4-mini cost = %+v", cost)
	}
}

func TestModelsList_AttachesDottedModelPriceOverride(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{
		"models": {
			"providers": {
				"openai": {
					"models": [
						{"id": "gpt-5.4-mini", "name": "GPT-5.4 mini"}
					]
				}
			},
			"openai/gpt-5.4-mini": {
				"priceUsdPer1M": {"in": 9.0, "out": 90.0}
			}
		}
	}`))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1: %+v", len(models), models)
	}
	cost := models[0]["cost"].(map[string]any)
	if cost["input"].(float64) != 9.0 || cost["output"].(float64) != 90.0 || cost["source"] != "catalog" {
		t.Errorf("cost override = %+v", cost)
	}
}

func TestModelsList_AttachesBuiltinModelPrice(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{
		"models": {
			"providers": {
				"openai": {
					"models": [
						{"id": "gpt-5.4-mini", "name": "GPT-5.4 mini"}
					]
				}
			}
		}
	}`))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1: %+v", len(models), models)
	}
	cost := models[0]["cost"].(map[string]any)
	if cost["input"].(float64) != 0.75 || cost["output"].(float64) != 4.50 || cost["cacheRead"].(float64) != 0.075 || cost["source"] != "builtin" {
		t.Errorf("builtin cost = %+v", cost)
	}
}

func TestModelsList_EmptyConfigReturnsEmpty(t *testing.T) {
	// With no in-tree catalog, no plugins loaded, and no user
	// config, models.list returns an empty list. The picker is
	// driven entirely by plugin ListProviderModels + user-config
	// overlays + LM Studio dynamic discovery — none active here.
	h := NewReadHandler(readFixture(t, `{}`))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	if len(models) != 0 {
		t.Errorf("empty config should produce zero rows, got %d: %v", len(models), models)
	}
}

func TestModelsList_IgnoresPluginManifestModels(t *testing.T) {
	// Plugins advertising models in their manifest must NOT show up
	// in the picker — the picker is config-driven only. Discovery
	// (ListProviderModels RPC) is also off this path by design.
	h := NewReadHandler(readFixture(t, `{}`))
	host := plugin.NewHost()
	if err := host.RegisterInstance(plugin.NewInstance(plugin.InstanceFields{
		Name: "anthropic",
		Manifest: &pb.Manifest{
			OffersProviders: []*pb.ProviderSpec{{
				Name:   "anthropic",
				Models: []string{"claude-opus-4-7"},
			}},
		},
	})); err != nil {
		t.Fatal(err)
	}
	h.WithHost(host)

	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	models := res.(map[string]any)["models"].([]map[string]any)
	if len(models) != 0 {
		t.Errorf("plugin manifest must not bleed into models.list; got %d rows: %v", len(models), models)
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

func TestConfigSchema_MissingCacheReturnsPermissiveEnvelope(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{}`))
	res, ferr := h.handleConfigSchema(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("missing cache should fall back, not error: %+v", ferr)
	}
	env, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("expected map envelope, got %T", res)
	}
	if env["generatedAt"] == nil {
		t.Errorf("envelope missing generatedAt: %+v", env)
	}
	schema, ok := env["schema"].(map[string]any)
	if !ok {
		t.Fatalf("envelope.schema not a map: %+v", env["schema"])
	}
	if schema["type"] != "object" {
		t.Errorf("schema.type = %v, want object", schema["type"])
	}
	if schema["additionalProperties"] != true {
		t.Errorf("permissive schema should set additionalProperties=true: %+v", schema)
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
		"agents.files.list":  false,
		"agents.files.get":   false,
		"agents.files.set":   false,
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
	// Representative IDENTITY.md content from the main agent workspace.
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
	h1 := NewReadHandler(readFixture(t, `{"gateway":{"port":18789}}`))
	h2 := NewReadHandler(readFixture(t, `{"gateway":{"port":18790}}`))
	r1, _ := h1.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	r2, _ := h2.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	if r1.(map[string]any)["hash"] == r2.(map[string]any)["hash"] {
		t.Errorf("hash should differ for different content")
	}
}

func TestConfigGet_ExistsReflectsTalonOverlay(t *testing.T) {
	h := NewReadHandler(readFixture(t, `{"a":1}`))
	res, _ := h.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	if !res.(map[string]any)["exists"].(bool) {
		t.Errorf("exists should be true when config.toml is present")
	}

	h = NewReadHandler(readFixture(t, ""))
	res, _ = h.handleConfigGet(t.Context(), HandlerCtx{}, nil)
	if res.(map[string]any)["exists"].(bool) {
		t.Errorf("exists should be false when config.toml is absent")
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
	// The UI may call with {} (no sessionKey, no agentId). Returning
	// BAD_REQUEST silently breaks the chat label because the UI swallows
	// the error. Default to "main" instead.
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
		[]byte("- **Name:** Clawdia\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	h := NewReadHandler(readFixture(t, body))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main","sessionKey":"agent:missing:missing"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["name"] != "Clawdia" {
		t.Errorf("expected explicit agentId=main to win, got %+v", res)
	}
}
