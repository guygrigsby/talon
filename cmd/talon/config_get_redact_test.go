package main

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRenderConfigGet_RedactsLeafTokenByDefault(t *testing.T) {
	// `config get gateway.auth.token` returns a string; under the
	// default reveal=false it must come back as [REDACTED] so we
	// don't leak credentials into the terminal scrollback (or worse,
	// into a chat transcript).
	res := gjson.Parse(`"abc123"`)
	got, redacted := renderConfigGetValue("gateway.auth.token", res, false, false)
	if !redacted {
		t.Error("expected redacted=true")
	}
	if strings.Contains(got, "abc123") {
		t.Errorf("token leaked in output: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("expected REDACTED placeholder, got %q", got)
	}
}

func TestRenderConfigGet_RevealReturnsCleartext(t *testing.T) {
	res := gjson.Parse(`"abc123"`)
	got, redacted := renderConfigGetValue("gateway.auth.token", res, false, true)
	if redacted {
		t.Error("reveal=true must not flag redacted")
	}
	if !strings.Contains(got, "abc123") {
		t.Errorf("reveal should pass through cleartext, got %q", got)
	}
}

func TestRenderConfigGet_NonSensitivePathPassthrough(t *testing.T) {
	// gateway.port is not a credential; must always pass through
	// regardless of reveal.
	res := gjson.Parse(`18790`)
	got, redacted := renderConfigGetValue("gateway.port", res, false, false)
	if redacted {
		t.Error("non-sensitive path should not be redacted")
	}
	if got != "18790" {
		t.Errorf("got %q, want \"18790\"", got)
	}
}

func TestRenderConfigGet_ObjectAtSensitivePathRedactsLeaves(t *testing.T) {
	// `config get gateway.auth` returns {mode, token}; the token
	// leaf must come back as [REDACTED] but mode must stay visible.
	res := gjson.Parse(`{"mode":"token","token":"abc123"}`)
	got, redacted := renderConfigGetValue("gateway.auth", res, false, false)
	if !redacted {
		t.Error("expected redacted=true for object containing sensitive leaf")
	}
	if strings.Contains(got, "abc123") {
		t.Errorf("token leaked in object: %q", got)
	}
	if !strings.Contains(got, `"mode": "token"`) {
		t.Errorf("mode field should be preserved: %q", got)
	}
}

func TestRenderConfigGet_ObjectWithNoSecretsUnchanged(t *testing.T) {
	res := gjson.Parse(`{"port":18790,"bind":"loopback"}`)
	got, redacted := renderConfigGetValue("gateway", res, false, false)
	if redacted {
		t.Errorf("plain object should not be flagged redacted: %q", got)
	}
	if !strings.Contains(got, "18790") || !strings.Contains(got, "loopback") {
		t.Errorf("non-secret values should pass through: %q", got)
	}
}
