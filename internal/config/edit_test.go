package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/tidwall/gjson"
)

// fixture builds a Paths backed by a temp dir. If openclawJSON is non-empty
// it's written to <dir>/openclaw/openclaw.json; otherwise the openclaw layer
// is "missing" (file does not exist). If talonJSON is non-empty it's written
// to <dir>/talon/openclaw.json; otherwise the talon overlay is "missing".
func fixture(t *testing.T, openclawJSON, talonJSON string) openclaw.Paths {
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
	talonCfg := filepath.Join(talonDir, "openclaw.json")
	openclawCfg := filepath.Join(openclawDir, "openclaw.json")
	if openclawJSON != "" {
		if err := os.WriteFile(openclawCfg, []byte(openclawJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if talonJSON != "" {
		if err := os.WriteFile(talonCfg, []byte(talonJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return openclaw.Paths{
		Talon:    openclaw.Layer{Dir: talonDir, Config: talonCfg},
		Openclaw: openclaw.Layer{Dir: openclawDir, Config: openclawCfg},
	}
}

func mustParse(t *testing.T, s string) []string {
	t.Helper()
	segs, err := ParsePath(s)
	if err != nil {
		t.Fatalf("parse path %q: %v", s, err)
	}
	return segs
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// --- merge layer ------------------------------------------------------------

func TestMergedBytes_TalonOverridesOpenclaw(t *testing.T) {
	p := fixture(t,
		`{"gateway":{"port":18789,"bind":"loopback"}}`,
		`{"gateway":{"port":19000}}`,
	)
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, "gateway.port").Int(); v != 19000 {
		t.Errorf("port = %d, want talon override 19000", v)
	}
	if v := gjson.GetBytes(merged, "gateway.bind").Str; v != "loopback" {
		t.Errorf("bind = %q, want openclaw fallthrough %q", v, "loopback")
	}
}

func TestMergedBytes_AgentsListMergeByID(t *testing.T) {
	p := fixture(t,
		`{"agents":{"list":[{"id":"main","model":"a"},{"id":"coding","model":"b"}]}}`,
		`{"agents":{"list":[{"id":"coding","model":"c"},{"id":"research","model":"d"}]}}`,
	)
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, `agents.list.#(id=="main").model`).Str; v != "a" {
		t.Errorf("main.model = %q, want preserved %q", v, "a")
	}
	if v := gjson.GetBytes(merged, `agents.list.#(id=="coding").model`).Str; v != "c" {
		t.Errorf("coding.model = %q, want talon override %q", v, "c")
	}
	if v := gjson.GetBytes(merged, `agents.list.#(id=="research").model`).Str; v != "d" {
		t.Errorf("research.model = %q, want talon-added %q", v, "d")
	}
}

func TestMergedBytes_OnlyOpenclaw(t *testing.T) {
	p := fixture(t, `{"a":1}`, "")
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, "a").Int(); v != 1 {
		t.Errorf("a = %d, want 1", v)
	}
}

func TestMergedBytes_OnlyTalon(t *testing.T) {
	p := fixture(t, "", `{"a":2}`)
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, "a").Int(); v != 2 {
		t.Errorf("a = %d, want 2", v)
	}
}

func TestMergedBytes_NeitherLayer(t *testing.T) {
	p := fixture(t, "", "")
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != "{}" {
		t.Errorf("merged = %s, want {}", merged)
	}
}

func TestMergedBytes_SkipOpenclaw(t *testing.T) {
	p := fixture(t, `{"a":1}`, `{"b":2}`)
	p.SkipOpenclaw = true
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(merged, "a").Exists() {
		t.Errorf("openclaw key 'a' should be hidden when SkipOpenclaw=true: %s", merged)
	}
	if v := gjson.GetBytes(merged, "b").Int(); v != 2 {
		t.Errorf("b = %d, want 2", v)
	}
}

func TestMergedBytes_NativeTOMLPreferredWithLegacySecretFallback(t *testing.T) {
	t.Setenv("TALON_CONFIG_PATH", "")
	p := fixture(t, "", `{
		"gateway": {"port": 18000, "auth": {"token": "legacy-gateway-token"}},
		"agents": {"list": [{"id": "coding", "workspace": "/tmp/legacy-subagent"}]},
		"plugins": {"entries": {
			"brave": {"config": {"webSearch": {"apiKey": "legacy-brave-key"}}},
			"openai-compat": {"config": {"providers": {"openai": {"apiKey": "legacy-openai-key"}}}}
		}},
		"channels": {"telegram": {"botToken": "legacy-telegram-token"}}
	}`)
	if err := os.WriteFile(filepath.Join(p.Talon.Dir, "config.toml"), []byte(`
[gateway]
mode = "local"
bind = "loopback"
port = 19000
auth_mode = "token"
auth_token_ref = "<redacted:literal>"

[agent]
model = "openai/gpt-4o-mini"
workspace = "/tmp/main"
tools_profile = "full"

[[subagents]]
id = "coding"
model = "anthropic/claude-opus-4-7"

[tools]
profile = "full"
web_search_enabled = true
web_search_provider = "brave"
web_search_api_key_ref = "<redacted:literal>"

[models.providers.openai]
api = "responses"
base_url = "https://api.openai.com/v1"
api_key_ref = "op://vault/openai/key"

[channels.telegram]
enabled = true
bot_token_ref = "<redacted:literal>"

[plugins]
enabled = ["brave", "openai-compat"]
load_paths = ["/plugins"]
legacy_openclaw_shim = true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, "gateway.port").Int(); v != 19000 {
		t.Errorf("gateway.port = %d, want native TOML value 19000", v)
	}
	if v := gjson.GetBytes(merged, "gateway.auth.token").Str; v != "legacy-gateway-token" {
		t.Errorf("gateway auth token = %q, want legacy fallback", v)
	}
	if v := gjson.GetBytes(merged, "channels.telegram.botToken").Str; v != "legacy-telegram-token" {
		t.Errorf("telegram token = %q, want legacy fallback", v)
	}
	if v := gjson.GetBytes(merged, "plugins.entries.brave.config.webSearch.apiKey").Str; v != "legacy-brave-key" {
		t.Errorf("brave api key = %q, want legacy fallback", v)
	}
	if v := gjson.GetBytes(merged, "plugins.entries.openai-compat.config.providers.openai.apiKey").Str; v != "op://vault/openai/key" {
		t.Errorf("openai key = %q, want native TOML ref", v)
	}
	if gjson.GetBytes(merged, `agents.list.#(id=="coding").workspace`).Exists() {
		t.Errorf("subagent workspace should not survive native TOML adaptation: %s", merged)
	}
}

func TestMergedBytes_NativeTOMLCacheInvalidates(t *testing.T) {
	t.Setenv("TALON_CONFIG_PATH", "")
	p := fixture(t, "", `{"gateway":{"port":18000}}`)
	native := filepath.Join(p.Talon.Dir, "config.toml")
	if err := os.WriteFile(native, []byte(`[gateway]
port = 19000
`), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, "gateway.port").Int(); v != 19000 {
		t.Fatalf("gateway.port = %d, want 19000", v)
	}
	if err := os.WriteFile(native, []byte(`[gateway]
port = 191234
`), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, err = MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, "gateway.port").Int(); v != 191234 {
		t.Fatalf("gateway.port = %d, want cache to see updated native TOML port 191234", v)
	}
}

// --- write-target = talon only ---------------------------------------------

func TestSet_WritesTalonOnly_OpenclawUntouched(t *testing.T) {
	openclawSrc := `{"gateway":{"port":18789}}`
	p := fixture(t, openclawSrc, "")
	if _, err := Set(p, mustParse(t, "gateway.port"), 19000, SetOpts{}); err != nil {
		t.Fatal(err)
	}
	// openclaw must NOT have changed.
	if got := strings.TrimSpace(readFile(t, p.Openclaw.Config)); got != openclawSrc {
		t.Errorf("openclaw was modified: %s", got)
	}
	// talon overlay should now exist with the new port.
	if v := gjson.Get(readFile(t, p.Talon.Config), "gateway.port").Int(); v != 19000 {
		t.Errorf("talon overlay gateway.port = %d, want 19000", v)
	}
	// merged view should reflect override.
	merged, _ := MergedBytes(p)
	if v := gjson.GetBytes(merged, "gateway.port").Int(); v != 19000 {
		t.Errorf("merged gateway.port = %d, want 19000", v)
	}
}

func TestSet_DryRunNoWrite(t *testing.T) {
	p := fixture(t, `{"gateway":{"port":18789}}`, "")
	res, err := Set(p, mustParse(t, "gateway.port"), 19000, SetOpts{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Wrote {
		t.Errorf("dry-run should not write")
	}
	if _, err := os.Stat(p.Talon.Config); err == nil {
		t.Errorf("dry-run created talon overlay file")
	}
}

func TestSet_BootstrapsTalonOverlay(t *testing.T) {
	// Both layers missing — Set should create the talon overlay.
	p := fixture(t, "", "")
	if _, err := Set(p, mustParse(t, "gateway.port"), 19000, SetOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Talon.Config); err != nil {
		t.Errorf("talon overlay was not created: %v", err)
	}
}

// --- protected-path guard checks the merged view ----------------------------

func TestSet_ProtectedPathGuard_RefusesOpenclawSourcedRemoval(t *testing.T) {
	// Openclaw declares two providers; talon overlay is empty. Trying to
	// replace models.providers with a single provider via talon should be
	// refused — the merged view shows we'd drop "deepseek".
	openclawSrc := `{"models":{"providers":{"openai":{"api":"x"},"deepseek":{"api":"y"}}}}`
	p := fixture(t, openclawSrc, "")
	patch := map[string]any{"openai": map[string]any{"api": "z"}}
	_, err := Set(p, mustParse(t, "models.providers"), patch, SetOpts{})
	if err == nil {
		t.Fatalf("expected protected-path error, got nil")
	}
	if !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("error should name 'deepseek': %v", err)
	}
}

func TestSet_ForceReplaceBypassesGuardButOpenclawStillVisible(t *testing.T) {
	// --replace is a guard escape hatch in the layered model. The talon
	// overlay gets the new map, but openclaw's deepseek is still merged
	// in (read-only layer). Removing deepseek from the merged view
	// requires a future tombstone mechanism.
	openclawSrc := `{"models":{"providers":{"openai":{"api":"x"},"deepseek":{"api":"y"}}}}`
	p := fixture(t, openclawSrc, "")
	patch := map[string]any{"openai": map[string]any{"api": "z"}}
	if _, err := Set(p, mustParse(t, "models.providers"), patch, SetOpts{Mode: SetForceReplace}); err != nil {
		t.Fatal(err)
	}
	overlay := readFile(t, p.Talon.Config)
	if v := gjson.Get(overlay, "models.providers.openai.api").Str; v != "z" {
		t.Errorf("talon overlay openai.api = %q, want %q", v, "z")
	}
	if gjson.Get(overlay, "models.providers.deepseek").Exists() {
		t.Errorf("talon overlay should not contain deepseek: %s", overlay)
	}
	merged, _ := MergedBytes(p)
	if v := gjson.GetBytes(merged, "models.providers.openai.api").Str; v != "z" {
		t.Errorf("merged openai.api = %q, want talon-override %q", v, "z")
	}
	if !gjson.GetBytes(merged, "models.providers.deepseek").Exists() {
		t.Errorf("merged deepseek should still be visible from openclaw layer: %s", merged)
	}
}

// --- gateway.auth pruning ---------------------------------------------------

func TestSet_GatewayAuthMode_PrunesTalonOverlay(t *testing.T) {
	p := fixture(t, "", `{"gateway":{"auth":{"mode":"token","token":"abc"}}}`)
	res, err := Set(p, mustParse(t, "gateway.auth.mode"), "password", SetOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p.Talon.Config)
	if gjson.Get(got, "gateway.auth.token").Exists() {
		t.Errorf("talon overlay token should be pruned: %s", got)
	}
	if !equalStrings(res.PrunedPaths, []string{"gateway.auth.token"}) {
		t.Errorf("PrunedPaths = %v, want [gateway.auth.token]", res.PrunedPaths)
	}
}

func TestSet_GatewayAuthMode_FlagsStaleOpenclawCredential(t *testing.T) {
	// Openclaw has token=X, talon overlay is empty. Setting mode=password
	// in talon prunes nothing on the talon side but flags the stale
	// openclaw token so the user knows merged view is still wrong.
	p := fixture(t,
		`{"gateway":{"auth":{"mode":"token","token":"openclaw-token"}}}`,
		"",
	)
	res, err := Set(p, mustParse(t, "gateway.auth.mode"), "password", SetOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PrunedPaths) != 0 {
		t.Errorf("nothing should be pruned from talon overlay (it's empty), got %v", res.PrunedPaths)
	}
	wantStale := []string{"gateway.auth.token"}
	if !equalStrings(res.StaleOpenclawPaths, wantStale) {
		t.Errorf("StaleOpenclawPaths = %v, want %v", res.StaleOpenclawPaths, wantStale)
	}
}

// --- backup rotation + audit -----------------------------------------------

func TestWriteOverlay_RotatesBackupsAndAppendsAudit(t *testing.T) {
	p := fixture(t, "", `{"gateway":{"port":1}}`)
	for i := 2; i <= 5; i++ {
		if _, err := Set(p, mustParse(t, "gateway.port"), i, SetOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	// .bak exists (latest)
	if _, err := os.Stat(p.Talon.ConfigBackupPath(0)); err != nil {
		t.Errorf(".bak missing: %v", err)
	}
	// .bak.1 exists (previous)
	if _, err := os.Stat(p.Talon.ConfigBackupPath(1)); err != nil {
		t.Errorf(".bak.1 missing: %v", err)
	}
	// .bak.2 exists (earlier)
	if _, err := os.Stat(p.Talon.ConfigBackupPath(2)); err != nil {
		t.Errorf(".bak.2 missing: %v", err)
	}
	// audit log exists with one line per write (4 writes in this test).
	auditBytes, err := os.ReadFile(p.Talon.ConfigAuditLogPath())
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(auditBytes)), "\n")
	if len(lines) != 4 {
		t.Errorf("audit log has %d lines, want 4", len(lines))
	}
	// Each line must be valid JSON with the expected shape.
	for _, line := range lines {
		var rec AuditRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("audit line is not valid JSON: %v\n%s", err, line)
			continue
		}
		if rec.Source != "talon-config-io" || rec.Event != "config.write" {
			t.Errorf("audit record shape wrong: %+v", rec)
		}
		if rec.NextHash == "" {
			t.Errorf("audit record missing nextHash: %+v", rec)
		}
	}
}

// --- whitespace minimization (talon-7vk) ------------------------------------

func TestSet_IdempotentSetIsNoOp(t *testing.T) {
	p := fixture(t, "", `{"gateway":{"port":1}}`)
	// First set: actually changes the value, writes overlay + .bak.
	res, err := Set(p, mustParse(t, "gateway.port"), 19000, SetOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Wrote {
		t.Fatalf("first set should write")
	}
	// Capture state after first write.
	after1, err := os.ReadFile(p.Talon.Config)
	if err != nil {
		t.Fatal(err)
	}
	bak1Stat, _ := os.Stat(p.Talon.ConfigBackupPath(0))
	bak2Stat, _ := os.Stat(p.Talon.ConfigBackupPath(1))
	auditAfter1, _ := os.ReadFile(p.Talon.ConfigAuditLogPath())

	// Second set with the same value: should short-circuit (no rotation, no
	// audit append, file bytes unchanged).
	res2, err := Set(p, mustParse(t, "gateway.port"), 19000, SetOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Wrote {
		t.Errorf("idempotent set should not report Wrote=true")
	}
	after2, err := os.ReadFile(p.Talon.Config)
	if err != nil {
		t.Fatal(err)
	}
	if string(after1) != string(after2) {
		t.Errorf("overlay bytes changed across idempotent set:\nbefore: %s\nafter:  %s", after1, after2)
	}
	bak1Stat2, _ := os.Stat(p.Talon.ConfigBackupPath(0))
	if bak1Stat == nil || bak1Stat2 == nil || bak1Stat.ModTime() != bak1Stat2.ModTime() {
		t.Errorf(".bak rotated on idempotent set (mtime changed)")
	}
	bak2Stat2, _ := os.Stat(p.Talon.ConfigBackupPath(1))
	// .bak.1 mtime should be unchanged. (May not exist after only one write —
	// allow that case.)
	if bak2Stat != nil && bak2Stat2 != nil && bak2Stat.ModTime() != bak2Stat2.ModTime() {
		t.Errorf(".bak.1 rotated on idempotent set")
	}
	auditAfter2, _ := os.ReadFile(p.Talon.ConfigAuditLogPath())
	if string(auditAfter1) != string(auditAfter2) {
		t.Errorf("audit log appended on idempotent set:\nbefore: %s\nafter:  %s", auditAfter1, auditAfter2)
	}
}

// TestSet_WriteFormatMatchesOpenclaw locks talon's overlay output to openclaw's
// canonical JSON.stringify(v, null, 2) + "\n" shape. Drift here breaks
// byte-for-byte cross-compat and gets called out by talon-7vk.
func TestSet_WriteFormatMatchesOpenclaw(t *testing.T) {
	p := fixture(t, "", "")
	if _, err := Set(p, mustParse(t, "logging.level"), "debug", SetOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(p, mustParse(t, "agents.defaults.workspace"), "/foo", SetOpts{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p.Talon.Config)
	if err != nil {
		t.Fatal(err)
	}
	// What openclaw would emit for the same logical content via Node:
	// JSON.stringify(value, null, 2) + "\n".
	want := "{\n" +
		"  \"logging\": {\n" +
		"    \"level\": \"debug\"\n" +
		"  },\n" +
		"  \"agents\": {\n" +
		"    \"defaults\": {\n" +
		"      \"workspace\": \"/foo\"\n" +
		"    }\n" +
		"  }\n" +
		"}\n"
	if string(got) != want {
		t.Errorf("overlay format diverged from openclaw canonical form:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestUnset_IdempotentWhenAlreadyDeleted(t *testing.T) {
	p := fixture(t, "", `{"a":1,"b":2}`)
	if err := Unset(p, mustParse(t, "a")); err != nil {
		t.Fatal(err)
	}
	after1, err := os.ReadFile(p.Talon.Config)
	if err != nil {
		t.Fatal(err)
	}
	auditAfter1, _ := os.ReadFile(p.Talon.ConfigAuditLogPath())
	// Second unset of "a" must error (already gone), not silently rotate
	// backups. The idempotent short-circuit only applies to value-equal
	// writes, not to commands targeting nonexistent paths.
	err = Unset(p, mustParse(t, "a"))
	if err == nil {
		t.Errorf("expected error unsetting an already-removed path")
	}
	after2, _ := os.ReadFile(p.Talon.Config)
	if string(after1) != string(after2) {
		t.Errorf("overlay changed after no-op unset")
	}
	auditAfter2, _ := os.ReadFile(p.Talon.ConfigAuditLogPath())
	if string(auditAfter1) != string(auditAfter2) {
		t.Errorf("audit log appended on no-op unset")
	}
}

// --- unset ------------------------------------------------------------------

func TestUnset_RemovesFromTalonOnly(t *testing.T) {
	p := fixture(t, `{"x":"openclaw"}`, `{"x":"talon"}`)
	if err := Unset(p, mustParse(t, "x")); err != nil {
		t.Fatal(err)
	}
	merged, _ := MergedBytes(p)
	if v := gjson.GetBytes(merged, "x").Str; v != "openclaw" {
		t.Errorf("merged x = %q, want fallthrough %q after unset", v, "openclaw")
	}
}

func TestUnset_ErrorWhenOnlyInOpenclaw(t *testing.T) {
	p := fixture(t, `{"x":1}`, "")
	err := Unset(p, mustParse(t, "x"))
	if err == nil {
		t.Fatalf("expected ErrNotInOverlay, got nil")
	}
	if !errors.Is(err, ErrNotInOverlay) {
		t.Errorf("error should wrap ErrNotInOverlay: %v", err)
	}
}

// --- validate ---------------------------------------------------------------

func TestValidate_RefreshesLastGood(t *testing.T) {
	p := fixture(t, "", `{"gateway":{"port":1}}`)
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Talon.LastGoodPath()); err != nil {
		t.Errorf("last-good not created: %v", err)
	}
}

// --- helpers ---------------------------------------------------------------

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
