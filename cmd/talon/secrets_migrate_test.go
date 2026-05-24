package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/openclaw"
)

func TestParseFileRef(t *testing.T) {
	cases := []struct {
		in      string
		wantRel string
		wantKey string
		wantErr bool
	}{
		{"file://agents/main/agent/auth-profiles.json:profiles.openai:default.key",
			"agents/main/agent/auth-profiles.json",
			"profiles.openai:default.key",
			false},
		{"file://identity/device-auth.json:tokens.operator.token",
			"identity/device-auth.json",
			"tokens.operator.token",
			false},
		// missing colon
		{"file://identity/device-auth.json", "", "", true},
		// missing key after colon
		{"file://identity/device-auth.json:", "", "", true},
		// empty rel
		{"file://:foo", "", "", true},
	}
	for _, c := range cases {
		rel, key, err := parseFileRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseFileRef(%q): expected error, got rel=%q key=%q", c.in, rel, key)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFileRef(%q): unexpected error %v", c.in, err)
			continue
		}
		if rel != c.wantRel || key != c.wantKey {
			t.Errorf("parseFileRef(%q) = (%q, %q), want (%q, %q)", c.in, rel, key, c.wantRel, c.wantKey)
		}
	}
}

func TestKeychainServiceForPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gateway.auth.token", "talon.gateway.auth.token"},
		{"channels.telegram.botToken", "talon.channels.telegram.botToken"},
		// file:// paths from auditFileSecrets use unquoted keys
		// (the colon inside "openai:default" is preserved as a
		// regular character — gjson/sjson treat it that way).
		{
			"file://agents/main/agent/auth-profiles.json:profiles.openai:default.key",
			"talon.agents.main.agent.auth-profiles.json.profiles.openai.default.key",
		},
		{
			"file://identity/device-auth.json:devices[0].token",
			"talon.identity.device-auth.json.devices.0.token",
		},
		{
			"a..b...c", // pre-collapsed input
			"talon.a.b.c",
		},
		{
			".gateway.token.", // leading/trailing dots get trimmed
			"talon.gateway.token",
		},
	}
	for _, tc := range cases {
		got := KeychainServiceForPath(tc.in)
		if got != tc.want {
			t.Errorf("KeychainServiceForPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// migratePaths returns a clean two-layer Paths for the test.
func migratePaths(t *testing.T) openclaw.Paths {
	t.Helper()
	talonDir := t.TempDir()
	openclawDir := t.TempDir()
	return openclaw.Paths{
		Talon:    openclaw.Layer{Dir: talonDir, Config: filepath.Join(talonDir, "openclaw.json")},
		Openclaw: openclaw.Layer{Dir: openclawDir, Config: filepath.Join(openclawDir, "openclaw.json")},
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestMigratePlan_EmptyWhenNoLiterals(t *testing.T) {
	paths := migratePaths(t)
	mustWrite(t, paths.Talon.Config, `{"gateway":{"auth":{"token":"keychain://existing"}}}`)

	items, err := buildMigratePlan(paths, "")
	if err != nil {
		t.Fatalf("buildMigratePlan: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty plan, got %d items: %+v", len(items), items)
	}
}

func TestMigratePlan_SkipsRefsAndEmpty(t *testing.T) {
	paths := migratePaths(t)
	mustWrite(t, paths.Talon.Config, `{
		"gateway": {"auth": {"token": "op://Personal/talon/token"}},
		"channels": {
			"telegram": {"botToken": ""},
			"slack":    {"token": "sk-plaintext-leak"}
		}
	}`)

	items, err := buildMigratePlan(paths, "")
	if err != nil {
		t.Fatalf("buildMigratePlan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %+v", len(items), items)
	}
	if items[0].Path != "channels.slack.token" {
		t.Errorf("path=%q, want channels.slack.token", items[0].Path)
	}
	if items[0].Service != "talon.channels.slack.token" {
		t.Errorf("service=%q", items[0].Service)
	}
	if items[0].Value != "sk-plaintext-leak" {
		t.Errorf("value not propagated to plan")
	}
}

func TestMigratePlan_FilterNarrowsResults(t *testing.T) {
	paths := migratePaths(t)
	mustWrite(t, paths.Talon.Config, `{
		"gateway":  {"auth": {"token": "sk-1"}},
		"channels": {"telegram": {"botToken": "sk-2"}, "slack": {"token": "sk-3"}}
	}`)

	all, _ := buildMigratePlan(paths, "")
	if len(all) != 3 {
		t.Fatalf("expected 3 items, got %d: %+v", len(all), all)
	}

	filtered, _ := buildMigratePlan(paths, "channels")
	if len(filtered) != 2 {
		t.Errorf("expected 2 channels items, got %d: %+v", len(filtered), filtered)
	}
	for _, it := range filtered {
		if !strings.Contains(it.Path, "channels") {
			t.Errorf("filter leak: %s", it.Path)
		}
	}
}

func TestMigratePlan_IncludesFileSecrets(t *testing.T) {
	paths := migratePaths(t)
	mustWrite(t, paths.Talon.Config, `{}`)
	mustWrite(t,
		filepath.Join(paths.Openclaw.Dir, "agents/main/agent/auth-profiles.json"),
		`{
			"version": 1,
			"profiles": {
				"openai:default": {"type":"api_key","provider":"openai","key":"sk-real"}
			}
		}`)

	items, err := buildMigratePlan(paths, "")
	if err != nil {
		t.Fatalf("buildMigratePlan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 file-secret item, got %d: %+v", len(items), items)
	}
	if !strings.HasPrefix(items[0].Path, "file://") {
		t.Errorf("path=%q should be file:// shape", items[0].Path)
	}
	if items[0].Value != "sk-real" {
		t.Errorf("value not propagated from auth-profiles.json")
	}
	if !strings.HasPrefix(items[0].Service, "talon.agents.main.") {
		t.Errorf("service=%q should contain agents.main", items[0].Service)
	}
}

// printMigratePlan must NEVER write the secret value to its output —
// only paths, service names, and the future ref. A regression here
// would leak credentials into shell history / log files / Slack
// pastes that include command output.
func TestMigratePlan_DoesNotEchoSecretValues(t *testing.T) {
	paths := migratePaths(t)
	const sentinel = "TOTALLY-NOT-A-SECRET-X9X9X9"
	mustWrite(t, paths.Talon.Config, `{"gateway":{"auth":{"token":"`+sentinel+`"}}}`)

	items, err := buildMigratePlan(paths, "")
	if err != nil {
		t.Fatalf("buildMigratePlan: %v", err)
	}
	var buf bytes.Buffer
	c := secretsMigrateCmd()
	c.SetOut(&buf)
	printMigratePlan(c, items, "test-account", false)
	if strings.Contains(buf.String(), sentinel) {
		t.Errorf("plan output leaked secret value:\n%s", buf.String())
	}
}

func TestWriteFileRef_PreservesOpenclawFile(t *testing.T) {
	paths := migratePaths(t)
	openclawFile := filepath.Join(paths.Openclaw.Dir, "agents/main/agent/auth-profiles.json")
	original := `{
		"version": 1,
		"profiles": {
			"openai:default": {"type":"api_key","provider":"openai","key":"sk-original"}
		}
	}`
	mustWrite(t, openclawFile, original)

	const fileRef = "file://agents/main/agent/auth-profiles.json:profiles.openai:default.key"
	const newRef = "keychain://talon.agents.main.openai.default"
	if err := writeFileRef(paths, fileRef, newRef); err != nil {
		t.Fatalf("writeFileRef: %v", err)
	}

	// openclaw layer must be byte-identical to what we wrote.
	got, err := os.ReadFile(openclawFile)
	if err != nil {
		t.Fatalf("read openclaw: %v", err)
	}
	if string(got) != original {
		t.Errorf("openclaw layer mutated:\nbefore=%q\nafter=%q", original, string(got))
	}

	// Talon overlay should now exist with the keychain:// ref.
	overlay, err := os.ReadFile(filepath.Join(paths.Talon.Dir, "agents/main/agent/auth-profiles.json"))
	if err != nil {
		t.Fatalf("read talon overlay: %v", err)
	}
	if !strings.Contains(string(overlay), newRef) {
		t.Errorf("overlay missing new ref: %s", string(overlay))
	}
	if strings.Contains(string(overlay), "sk-original") {
		t.Errorf("overlay still contains old literal: %s", string(overlay))
	}
}
