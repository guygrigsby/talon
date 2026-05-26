package agentcore_chat

import (
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/talonpath"
)

// clearProviderEnv wipes provider-related env vars so tests don't
// pick up real keys from the developer's shell. We can't restore
// them inside go test without t.Setenv (which only does set/swap),
// but the test process exits after the suite anyway.
//
// CRITICAL: never %v/%+v a ProviderAuth in test output. The struct
// has APIKey as a field. Use AuthFingerprint() or compare booleans.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, e := range []string{
		"OPENAI_API_KEY", "OPENAI_API_KEY_REF",
		"DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY_REF",
		"MISTRAL_API_KEY", "MISTRAL_API_KEY_REF",
		"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_REF",
		"MLX_API_KEY", "LMSTUDIO_API_KEY", "OLLAMA_API_KEY",
	} {
		t.Setenv(e, "")
	}
}

func TestResolveProviderAuth_LiteralKeysPassThrough(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"plugins": {
			"entries": {
				"openai-compat": {
					"config": {"providers": {
						"openai":   {"baseUrl": "https://api.openai.com/v1",   "apiKey": "sk-test-openai"},
						"deepseek": {"baseUrl": "https://api.deepseek.com/v1", "apiKey": "sk-test-deepseek"}
					}}
				},
				"anthropic": {"config": {"apiKey": "sk-ant-test"}}
			}
		}
	}`)
	got := ResolveProviderAuth(cfg, talonpath.Paths{})
	if a, ok := got["openai"]; !ok || a.APIKey != "sk-test-openai" || a.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("openai entry wrong (key/url mismatch, present=%v)", ok)
	}
	if a, ok := got["deepseek"]; !ok || a.APIKey != "sk-test-deepseek" {
		t.Errorf("deepseek entry wrong (present=%v, key matches=%v)", ok, a.APIKey == "sk-test-deepseek")
	}
	if a, ok := got["anthropic"]; !ok || a.APIKey != "sk-ant-test" {
		t.Errorf("anthropic entry wrong (present=%v, key matches=%v)", ok, a.APIKey == "sk-ant-test")
	}
}

func TestResolveProviderAuth_LoopbackKeepsEntryWithoutKey(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"plugins": {"entries": {"openai-compat": {"config": {"providers": {
			"mlx":      {"baseUrl": "http://localhost:8080/v1"},
			"lmstudio": {"baseUrl": "http://127.0.0.1:1234/v1"},
			"ollama":   {"baseUrl": "http://[::1]:11434/v1"}
		}}}}}
	}`)
	got := ResolveProviderAuth(cfg, talonpath.Paths{})
	for _, name := range []string{"mlx", "lmstudio", "ollama"} {
		if _, ok := got[name]; !ok {
			t.Errorf("loopback provider %q should register even without an API key", name)
		}
	}
}

func TestResolveProviderAuth_NonLoopbackSkippedWithoutKey(t *testing.T) {
	clearProviderEnv(t)
	cfg := []byte(`{
		"plugins": {"entries": {"openai-compat": {"config": {"providers": {
			"mistral": {"baseUrl": "https://api.mistral.ai/v1"}
		}}}}}
	}`)
	got := ResolveProviderAuth(cfg, talonpath.Paths{})
	if _, present := got["mistral"]; present {
		// SAFETY: never print the resolved struct; it contains the
		// APIKey. Boolean assertion only.
		t.Errorf("non-loopback provider without key should be omitted but was present")
	}
}

func TestResolveProviderAuth_EnvFallback(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	cfg := []byte(`{
		"plugins": {"entries": {"openai-compat": {"config": {"providers": {
			"openai": {"baseUrl": "https://api.openai.com/v1"}
		}}}}}
	}`)
	got := ResolveProviderAuth(cfg, talonpath.Paths{})
	if a, ok := got["openai"]; !ok || a.APIKey != "sk-from-env" {
		t.Errorf("env fallback should populate APIKey (present=%v, match=%v)", ok, a.APIKey == "sk-from-env")
	}
}

func TestResolveProviderAuth_RefDoesNotResolveInTestEnv(t *testing.T) {
	clearProviderEnv(t)
	// Without a real op-plugin + keychain, op:// refs can't
	// resolve. The provider should be omitted (no APIKey, not
	// loopback) — silent failure is the production behavior too;
	// the auth-status RPC surfaces it on the FE.
	cfg := []byte(`{
		"plugins": {"entries": {"openai-compat": {"config": {"providers": {
			"openai": {"baseUrl": "https://api.openai.com/v1", "apiKey": "op://no-such-vault/no-such-item/credential"}
		}}}}}
	}`)
	got := ResolveProviderAuth(cfg, talonpath.Paths{})
	if _, present := got["openai"]; present {
		t.Errorf("unresolvable ref should skip the provider but was present")
	}
}

func TestAuthFingerprint_StableAndSafe(t *testing.T) {
	a := ProviderAuth{Provider: "openai", APIKey: "sk-test-1"}
	fp1 := AuthFingerprint(a)
	fp2 := AuthFingerprint(a)
	if fp1 != fp2 {
		t.Errorf("fingerprint not stable: %q vs %q", fp1, fp2)
	}
	if fp1 == "" || strings.Contains(fp1, a.APIKey) {
		t.Errorf("fingerprint must not leak key: %q", fp1)
	}
	if !strings.HasPrefix(fp1, "openai:") {
		t.Errorf("fingerprint missing provider prefix: %q", fp1)
	}

	// Empty key → empty fingerprint, never leaks "provider:".
	if got := AuthFingerprint(ProviderAuth{Provider: "anthropic"}); got != "" {
		t.Errorf("empty key should produce empty fingerprint, got %q", got)
	}

	// Different key → different fingerprint.
	other := AuthFingerprint(ProviderAuth{Provider: "openai", APIKey: "sk-test-2"})
	if other == fp1 {
		t.Errorf("different keys produced same fingerprint")
	}
}

func TestIsLoopbackURL(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:8080/v1":      true,
		"http://127.0.0.1:1234/v1":      true,
		"http://[::1]:11434/v1":         true,
		"http://0.0.0.0:1234/v1":        true,
		"https://api.openai.com/v1":     false,
		"https://api.deepseek.com/v1":   false,
		"http://192.168.1.10:1234/v1":   false,
		"":                              false,
	}
	for u, want := range cases {
		if got := isLoopbackURL(u); got != want {
			t.Errorf("isLoopbackURL(%q) = %v, want %v", u, got, want)
		}
	}
}
