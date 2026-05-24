package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/openclaw"
)

// newAuthStatusPaths builds a clean two-layer Paths rooted in t.TempDir()
// so each test gets its own isolated overlay + openclaw layers.
func newAuthStatusPaths(t *testing.T) openclaw.Paths {
	t.Helper()
	talonDir := t.TempDir()
	openclawDir := t.TempDir()
	return openclaw.Paths{
		Talon:    openclaw.Layer{Dir: talonDir, Config: filepath.Join(talonDir, "openclaw.json")},
		Openclaw: openclaw.Layer{Dir: openclawDir, Config: filepath.Join(openclawDir, "openclaw.json")},
	}
}

func writeConfig(t *testing.T, layer openclaw.Layer, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(layer.Config), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(layer.Config, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func writeAuthProfiles(t *testing.T, layer openclaw.Layer, agentID, body string) {
	t.Helper()
	dir := filepath.Join(layer.AgentDir(agentID), "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir auth-profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth-profiles.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write auth-profiles: %v", err)
	}
}

func callAuthStatus(t *testing.T, paths openclaw.Paths, params string) map[string]any {
	t.Helper()
	r := NewRegistry()
	NewModelsAuthStatusHandler(paths).Register(r)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	res, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "models.authStatus", raw)
	if ferr != nil {
		t.Fatalf("dispatch errored: %+v", ferr)
	}
	got, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("response is not a map: %#v", res)
	}
	return got
}

func providerNames(t *testing.T, got map[string]any) []string {
	t.Helper()
	providers, ok := got["providers"].([]any)
	if !ok {
		t.Fatalf("response.providers is not an array: %#v", got)
	}
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("provider entry is not a map: %#v", p)
		}
		name, _ := m["provider"].(string)
		names = append(names, name)
	}
	return names
}

// Empty install: no config providers, no auth-profiles → empty list.
// ts is still a real epoch so the UI doesn't render "loaded at 1970."
func TestModelsAuthStatus_EmptyInstall(t *testing.T) {
	paths := newAuthStatusPaths(t)

	before := time.Now().UnixMilli()
	got := callAuthStatus(t, paths, `{}`)
	after := time.Now().UnixMilli()

	names := providerNames(t, got)
	if len(names) != 0 {
		t.Errorf("expected empty providers, got %v", names)
	}
	ts, ok := got["ts"].(int64)
	if !ok || ts < before || ts > after {
		t.Errorf("ts %v not a fresh epoch in [%d, %d]", got["ts"], before, after)
	}
}

// Provider listed in models.providers.* but no auth-profiles entry →
// status="missing" so the UI's attention card flags it.
func TestModelsAuthStatus_ConfiguredButUnauthed(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeConfig(t, paths.Talon, `{"models":{"providers":{"openai":{"models":[]}}}}`)

	got := callAuthStatus(t, paths, `{}`)
	providers, _ := got["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d: %#v", len(providers), providers)
	}
	entry := providers[0].(map[string]any)
	if entry["provider"] != "openai" {
		t.Errorf("provider=%v, want openai", entry["provider"])
	}
	if entry["status"] != "missing" {
		t.Errorf("status=%v, want missing", entry["status"])
	}
	if entry["displayName"] != "OpenAI" {
		t.Errorf("displayName=%v, want OpenAI", entry["displayName"])
	}
	profiles, _ := entry["profiles"].([]any)
	if len(profiles) != 0 {
		t.Errorf("expected no profiles, got %#v", profiles)
	}
}

// auth-profiles.json with a non-empty key → status="ok" and a
// matching profile entry with type="api_key" and status="ok".
func TestModelsAuthStatus_OKWhenKeyPresent(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeConfig(t, paths.Talon, `{"models":{"providers":{"openai":{}}}}`)
	writeAuthProfiles(t, paths.Openclaw, "main", `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":"sk-test-12345"}
		}
	}`)

	got := callAuthStatus(t, paths, `{}`)
	providers, _ := got["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	entry := providers[0].(map[string]any)
	if entry["status"] != "ok" {
		t.Errorf("provider.status=%v, want ok", entry["status"])
	}
	profiles, _ := entry["profiles"].([]any)
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	prof := profiles[0].(map[string]any)
	if prof["profileId"] != "openai:default" {
		t.Errorf("profileId=%v", prof["profileId"])
	}
	if prof["type"] != "api_key" {
		t.Errorf("type=%v, want api_key", prof["type"])
	}
	if prof["status"] != "ok" {
		t.Errorf("profile.status=%v, want ok", prof["status"])
	}
}

// Profile present but key is empty → both profile.status and
// provider.status are "missing". Whitespace-only keys count as empty.
func TestModelsAuthStatus_MissingWhenKeyEmpty(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeAuthProfiles(t, paths.Openclaw, "main", `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":"   "}
		}
	}`)

	got := callAuthStatus(t, paths, `{}`)
	providers, _ := got["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	entry := providers[0].(map[string]any)
	if entry["status"] != "missing" {
		t.Errorf("provider.status=%v, want missing", entry["status"])
	}
	profiles, _ := entry["profiles"].([]any)
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	if profiles[0].(map[string]any)["status"] != "missing" {
		t.Errorf("profile.status=%v, want missing", profiles[0].(map[string]any)["status"])
	}
}

// op://... secret refs are non-empty → treated as configured (ok).
// We don't resolve the ref here; presence is what matters for the
// dashboard's "did the user wire this up?" question.
func TestModelsAuthStatus_SecretRefCountsAsOK(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeAuthProfiles(t, paths.Openclaw, "main", `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":"op://Personal/openai/api-key"}
		}
	}`)

	got := callAuthStatus(t, paths, `{}`)
	providers, _ := got["providers"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["status"] != "ok" {
		t.Errorf("expected single ok provider, got %#v", providers)
	}
}

// talon overlay wins over openclaw layer. A key in ~/.talon shadows
// a different (or missing) key in ~/.openclaw.
func TestModelsAuthStatus_TalonOverlayWinsOverOpenclaw(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeAuthProfiles(t, paths.Openclaw, "main", `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":""}
		}
	}`)
	writeAuthProfiles(t, paths.Talon, "main", `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":"sk-overlay"}
		}
	}`)

	got := callAuthStatus(t, paths, `{}`)
	providers, _ := got["providers"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["status"] != "ok" {
		t.Errorf("expected talon overlay ok, got %#v", providers)
	}
}

// Union of providers from models.providers.* and auth-profiles.json.
// A provider that appears only in auth-profiles still surfaces, and
// a provider that appears only in config still surfaces as missing.
func TestModelsAuthStatus_UnionsConfigAndProfiles(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeConfig(t, paths.Talon, `{"models":{"providers":{"openai":{},"anthropic":{}}}}`)
	writeAuthProfiles(t, paths.Openclaw, "main", `{
		"version": 1,
		"profiles": {
			"openai:default":   {"type":"api_key","provider":"openai","key":"sk-1"},
			"deepseek:default": {"type":"api_key","provider":"deepseek","key":"sk-2"}
		}
	}`)

	got := callAuthStatus(t, paths, `{}`)
	names := providerNames(t, got)
	sort.Strings(names)
	want := []string{"anthropic", "deepseek", "openai"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("providers=%v, want %v", names, want)
	}
}

// agentId param picks the auth-profiles directory: keys for a
// non-default agent must not leak into "main"'s response.
func TestModelsAuthStatus_AgentIDScoping(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeAuthProfiles(t, paths.Openclaw, "scratch", `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":"sk-scratch"}
		}
	}`)

	gotMain := callAuthStatus(t, paths, `{"agentId":"main"}`)
	if names := providerNames(t, gotMain); len(names) != 0 {
		t.Errorf("main agent should see no providers, got %v", names)
	}
	gotScratch := callAuthStatus(t, paths, `{"agentId":"scratch"}`)
	providers, _ := gotScratch["providers"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["status"] != "ok" {
		t.Errorf("scratch agent should see ok openai, got %#v", providers)
	}
}

// provider filter narrows the response to a single entry.
func TestModelsAuthStatus_ProviderFilter(t *testing.T) {
	paths := newAuthStatusPaths(t)
	writeConfig(t, paths.Talon, `{"models":{"providers":{"openai":{},"anthropic":{},"deepseek":{}}}}`)

	got := callAuthStatus(t, paths, `{"provider":"anthropic"}`)
	names := providerNames(t, got)
	if !reflect.DeepEqual(names, []string{"anthropic"}) {
		t.Errorf("providers=%v, want [anthropic]", names)
	}
}

// Tolerant param parsing: openclaw's UI sends {} or {refresh:true}
// today; tests cover nil/null and a garbage-shape body for robustness.
func TestModelsAuthStatus_AcceptsVariedParams(t *testing.T) {
	paths := newAuthStatusPaths(t)
	r := NewRegistry()
	NewModelsAuthStatusHandler(paths).Register(r)

	cases := []json.RawMessage{
		nil,
		json.RawMessage("null"),
		json.RawMessage(`{}`),
		json.RawMessage(`{"refresh":true}`),
		json.RawMessage(`{"agentId":"main","provider":"openai","refresh":false}`),
		json.RawMessage(`"not an object"`), // tolerated, treated as {}
	}
	for _, params := range cases {
		_, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "models.authStatus", params)
		if ferr != nil {
			t.Errorf("params=%q failed: %+v", string(params), ferr)
		}
	}
}
