package secrets

import (
	"strings"
	"testing"
)

func TestIsSensitiveKey(t *testing.T) {
	cases := map[string]bool{
		// Should match (real secrets)
		"token":          true,
		"botToken":       true,
		"BOT_TOKEN":      true,
		"refreshToken":   true,
		"apiKey":         true,
		"api_key":        true,
		"apikey":         true,
		"clientSecret":   true,
		"PRIVATE_KEY":    true,
		"signing_key":    true,
		"auth":           true,
		"password":       true,
		// Should NOT match — these were false-positives from the
		// initial substring-only impl. maxTokens contains "token"
		// as a substring but is a numeric limit; allowInsecureAuth
		// has the word "auth" but is a boolean toggle, not a
		// credential.
		"maxTokens":           false,
		"maxTokensField":      false,
		"compat.maxTokens":    false, // dotted variant from gjson keys
		"port":                false,
		"name":                false,
		"agentId":             false,
		"enabled":             false,
		"":                    false,
	}
	for k, want := range cases {
		if got := IsSensitiveKey(k); got != want {
			t.Errorf("IsSensitiveKey(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestIsSensitivePath(t *testing.T) {
	cases := map[string]bool{
		"gateway.auth.token":      true,
		"gateway.auth":            true, // "auth" segment matches
		"channels.telegram.botToken": true,
		"agents.list[0].auth":     true,
		"gateway.port":            false,
		"agents.defaults.model.primary": false,
		"":                        false,
	}
	for p, want := range cases {
		if got := IsSensitivePath(p); got != want {
			t.Errorf("IsSensitivePath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestRedactJSON_RedactsLeavesAndPreservesStructure(t *testing.T) {
	in := `{
		"gateway": {"port": 18790, "auth": {"mode": "token", "token": "abc"}},
		"channels": {"telegram": {"botToken": "xx", "agentId": "main", "enabled": true}}
	}`
	out, err := RedactJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, want := range []string{
		`"token": "[REDACTED]"`,
		`"botToken": "[REDACTED]"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, leak := range []string{"abc", "xx"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in:\n%s", leak, got)
		}
	}
	for _, keep := range []string{`"port": 18790`, `"mode": "token"`, `"enabled": true`, `"agentId": "main"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("clobbered non-secret %q in:\n%s", keep, got)
		}
	}
}

func TestRedactJSON_PreservesEmptyValues(t *testing.T) {
	// Empty token is a "signal: configured but blank" marker —
	// don't lie about what's set.
	in := `{"gateway": {"auth": {"token": ""}}}`
	out, _ := RedactJSON([]byte(in))
	if !strings.Contains(string(out), `"token": ""`) {
		t.Errorf("empty token should not be redacted:\n%s", out)
	}
}

func TestRedactValueForKey(t *testing.T) {
	cases := []struct {
		key, val, want string
	}{
		{"token", "abc123", "[REDACTED]"},
		{"botToken", "xx", "[REDACTED]"},
		{"port", "18790", "18790"},
		{"name", "main", "main"},
		{"token", "", ""}, // empty passes through
	}
	for _, c := range cases {
		if got := RedactValueForKey(c.key, c.val); got != c.want {
			t.Errorf("RedactValueForKey(%q, %q) = %q, want %q", c.key, c.val, got, c.want)
		}
	}
}

func TestWalk_NoOpOnNonContainers(t *testing.T) {
	// Defense-in-depth — calling Walk on scalars must not panic.
	for _, v := range []any{nil, "x", 42, true, []any{1, 2, 3}} {
		Walk(v, "")
	}
}
