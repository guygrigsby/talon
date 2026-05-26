package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

func configFixture(t *testing.T, runtimeJSON string) talonpath.Paths {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if runtimeJSON != "" {
		cfg, err := talonconfig.FromRuntimeJSON([]byte(runtimeJSON))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return talonpath.Paths{Talon: talonpath.Layer{Dir: dir, Config: cfgPath}}
}

func fixture(t *testing.T, baseJSON, overlayJSON string) talonpath.Paths {
	if overlayJSON != "" {
		return configFixture(t, overlayJSON)
	}
	return configFixture(t, baseJSON)
}

func TestMergedBytes_ReadsNativeTOML(t *testing.T) {
	p := configFixture(t, `{
		"gateway": {"port": 18000, "auth": {"mode": "token", "token": "op://vault/item/token"}},
		"agents": {"defaults": {"model": {"primary": "openai/gpt-4o-mini"}}, "list": [{"id": "main"}]},
		"models": {"providers": {"openai": {"api": "openai", "models": [{"id": "gpt-4o-mini"}]}}}
	}`)
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(merged, "gateway.port").Int(); v != 18000 {
		t.Errorf("gateway.port = %d, want 18000", v)
	}
	if v := gjson.GetBytes(merged, "agents.defaults.model.primary").Str; v != "openai/gpt-4o-mini" {
		t.Errorf("primary model = %q", v)
	}
}

func TestSet_WritesNativeTOML(t *testing.T) {
	p := configFixture(t, `{"gateway":{"port":18789},"agents":{"list":[{"id":"main"}]}}`)
	res, err := Set(p, []string{"gateway", "port"}, float64(19000), SetOpts{Mode: SetReplaceSafe})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Wrote {
		t.Fatal("expected write")
	}
	raw, err := os.ReadFile(p.Talon.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "port = 19000") {
		t.Fatalf("config.toml was not updated:\n%s", raw)
	}
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(merged, "gateway.port").Int(); got != 19000 {
		t.Errorf("merged gateway.port = %d, want 19000", got)
	}
}

func TestSet_GatewayAuthModePrunesInactiveCredentials(t *testing.T) {
	p := configFixture(t, `{"gateway":{"auth":{"mode":"token","token":"keychain://talon.gateway.token","password":"keychain://talon.gateway.password"}}}`)
	res, err := Set(p, []string{"gateway", "auth", "mode"}, "password", SetOpts{Mode: SetReplaceSafe})
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(res.PrunedPaths, []string{"gateway.auth.token"}) {
		t.Fatalf("PrunedPaths = %v", res.PrunedPaths)
	}
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(merged, "gateway.auth.token").Exists() {
		t.Fatalf("token should have been pruned: %s", merged)
	}
	if !gjson.GetBytes(merged, "gateway.auth.password").Exists() {
		t.Fatalf("password should remain: %s", merged)
	}
}

func TestSet_RejectsPlaintextSecretString(t *testing.T) {
	p := configFixture(t, `{}`)
	_, err := Set(p, []string{"gateway", "auth", "token"}, "plain-token", SetOpts{Mode: SetReplaceSafe})
	if err == nil {
		t.Fatal("expected plaintext secret write to be rejected")
	}
	if !strings.Contains(err.Error(), "op:// or keychain://") {
		t.Fatalf("error should explain secret refs, got %v", err)
	}
}

func TestSet_AllowsSecretStoreReference(t *testing.T) {
	p := configFixture(t, `{}`)
	if _, err := Set(p, []string{"gateway", "auth", "token"}, "op://Personal/talon-gateway/credential", SetOpts{Mode: SetReplaceSafe}); err != nil {
		t.Fatalf("secret ref should be allowed: %v", err)
	}
}

func TestSet_RejectsPlaintextSecretInObject(t *testing.T) {
	p := configFixture(t, `{}`)
	_, err := Set(p, []string{"plugins", "entries", "brave", "config"}, map[string]any{
		"webSearch": map[string]any{"apiKey": "plain-key"},
	}, SetOpts{Mode: SetMerge})
	if err == nil {
		t.Fatal("expected nested plaintext secret write to be rejected")
	}
	if !strings.Contains(err.Error(), "plugins.entries.brave.config.webSearch.apiKey") {
		t.Fatalf("error should name nested path, got %v", err)
	}
}

func TestSet_AuthModeIsNotSecret(t *testing.T) {
	p := configFixture(t, `{}`)
	if _, err := Set(p, []string{"gateway", "auth", "mode"}, "token", SetOpts{Mode: SetReplaceSafe}); err != nil {
		t.Fatalf("auth mode should not be treated as a credential: %v", err)
	}
}

func TestUnset_RemovesPath(t *testing.T) {
	p := configFixture(t, `{"gateway":{"auth":{"mode":"token","token":"keychain://talon.gateway.token"}}}`)
	if err := Unset(p, []string{"gateway", "auth", "token"}); err != nil {
		t.Fatal(err)
	}
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(merged, "gateway.auth.token").Exists() {
		t.Fatalf("token should be absent: %s", merged)
	}
}

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
