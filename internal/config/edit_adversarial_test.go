package config

import (
	"strings"
	"testing"
)

// Adversarial tests for config.Set — the authenticated config-mutation path
// behind ConfigService.Set. The headline concern is ADR 0006: a plaintext
// secret must never be persisted. Secondary: hostile dotted paths must not
// panic or inject structure outside the targeted key.

// Plaintext written to a sensitively-named key must be refused, in both
// replace and merge modes. These are fresh keys so the non-destructive guard
// stays out of the way and only the secret guard can fire.
func TestConfigSet_RefusesPlaintextSecrets(t *testing.T) {
	secretKeys := [][]string{
		{"gateway", "auth_token"},
		{"models", "providers", "acme", "api_key"},
		{"x", "password"},
		{"x", "secret"},
		{"x", "credential"},
		{"x", "signing_key"},
		{"x", "jwt"},
		{"x", "refresh_token"},
	}
	for _, mode := range []SetMode{SetReplaceSafe, SetMerge} {
		for _, segs := range secretKeys {
			p := configFixture(t, "")
			_, err := Set(p, segs, "sk-totally-plaintext-secret", SetOpts{Mode: mode})
			if err == nil {
				t.Errorf("ADR 0006 BREACH: plaintext accepted at %v (mode %v)", segs, mode)
				continue
			}
			if !strings.Contains(err.Error(), "plaintext secret") {
				t.Errorf("path %v: expected plaintext-secret refusal, got: %v", segs, err)
			}
		}
	}
}

// Reference values (op:// / keychain://) are the sanctioned way to set a
// secret and must be allowed.
func TestConfigSet_AllowsSecretReferences(t *testing.T) {
	for _, ref := range []string{"op://vault/item/field", "keychain://talon/auth-token"} {
		p := configFixture(t, "")
		if _, err := Set(p, []string{"gateway", "auth_token"}, ref, SetOpts{Mode: SetReplaceSafe}); err != nil {
			t.Errorf("reference %q should be allowed, got: %v", ref, err)
		}
	}
}

// FINDING (low severity): the secret guard's only non-sensitive qualifier is
// "public", and it short-circuits the whole key. So a key like "public_token"
// is treated as non-sensitive and a plaintext secret written there is NOT
// refused. Documents the bypass: a secret-bearing field named with "public"
// evades ADR 0006 enforcement. Realistic exposure is small (you'd have to name
// a secret field "public_*"), but the test pins the behavior.
func TestConfigSet_PublicQualifierBypassesSecretGuard(t *testing.T) {
	p := configFixture(t, "")
	_, err := Set(p, []string{"x", "public_token"}, "sk-this-is-really-secret", SetOpts{Mode: SetReplaceSafe})
	if err != nil {
		t.Logf("guard now rejects public_* keys (gap closed): %v", err)
		return
	}
	t.Logf("FINDING: plaintext secret accepted at x.public_token — 'public' qualifier negates the secret guard")
}

// The guard is key-name-based only: a genuinely secret value stored under a
// non-secret-sounding key sails through. Inherent to a heuristic; documented
// so nobody assumes config.Set scrubs values.
func TestConfigSet_NonSecretNamedKeyStoresPlaintext(t *testing.T) {
	p := configFixture(t, "")
	if _, err := Set(p, []string{"x", "note"}, "my password is hunter2 and api key sk-abc", SetOpts{Mode: SetReplaceSafe}); err != nil {
		t.Errorf("non-secret key should accept any string, got: %v", err)
	}
}

// ParsePath must never panic on hostile input and must reject the clearly
// malformed rather than producing garbage segments.
func TestParsePath_Hostile(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", false}, // empty -> nil segments, no error
		{"   ", false},
		{"...", false},        // all separators collapse to nothing
		{"a..b", false},       // empty middle segment dropped
		{"[]", true},          // empty bracket
		{"a[", true},          // unterminated bracket
		{"a[0", true},         // unterminated bracket
		{`a\.b`, false},       // escaped dot -> single literal-dot segment
		{"a[0].b", false},     // valid index + key
		{"a.b.c.d.e.f.g", false},
		{strings.Repeat("a.", 5000) + "z", false}, // very deep, must not blow up
		{"a[999999999999999999999]", false},       // absurd index: parsed as a string seg, not a panic
		{"\x00\x01\x02", false},                    // control bytes -> a (weird) segment, no panic
	}
	for _, c := range cases {
		segs, err := ParsePath(c.in) // must not panic
		if c.wantErr && err == nil {
			t.Errorf("ParsePath(%q) = %v, expected error", c.in, segs)
		}
		if !c.wantErr && err != nil {
			t.Errorf("ParsePath(%q) unexpected error: %v", c.in, err)
		}
	}
}

// A path segment containing a literal dot (from an escaped path like `a\.b`)
// must be written as a single key, not injected as nested structure. If
// ToSjsonPath failed to escape it, setting "a.b" would create {"a":{"b":...}}
// and silently write to the wrong place. Round-trip and confirm containment.
func TestConfigSet_LiteralDotSegmentNoInjection(t *testing.T) {
	p := configFixture(t, "")
	segs, err := ParsePath(`weird\.dotted`) // -> ["weird.dotted"], one segment
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	if len(segs) != 1 || segs[0] != "weird.dotted" {
		t.Fatalf("escaped-dot path should be one literal segment, got %v", segs)
	}
	// Non-secret string value so the secret guard doesn't intervene.
	if _, err := Set(p, segs, "value", SetOpts{Mode: SetReplaceSafe}); err != nil {
		t.Fatalf("Set with literal-dot segment: %v", err)
	}
	merged, err := MergedBytes(p)
	if err != nil {
		t.Fatalf("MergedBytes: %v", err)
	}
	// Must NOT have created a nested {"weird":{"dotted":...}} object.
	if strings.Contains(string(merged), `"weird":{`) {
		t.Errorf("PATH INJECTION: literal-dot segment created nested structure: %s", merged)
	}
}
