package server

import (
	"encoding/json"
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

func TestReadHandler_RegisterAddsAllThree(t *testing.T) {
	r := NewRegistry()
	NewReadHandler(readFixture(t, `{}`)).Register(r)
	want := map[string]bool{"agents.list": false, "models.list": false, "config.schema": false}
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
