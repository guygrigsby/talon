package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildUIURL_NonDefaultPortKeepsGatewayFragment(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18790, "", "", "/chat")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("invalid URL produced: %v\n%s", err, got)
	}
	if parsed.Host != "localhost:5173" {
		t.Errorf("host = %q, want localhost:5173", parsed.Host)
	}
	if parsed.Path != "/chat" {
		t.Errorf("path = %q, want /chat", parsed.Path)
	}
	frag, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("fragment not query-shaped: %v", err)
	}
	if frag.Get("gatewayUrl") != "ws://localhost:18790" {
		t.Errorf("gatewayUrl fragment = %q, want ws://localhost:18790 (non-default port)", frag.Get("gatewayUrl"))
	}
	if frag.Has("token") {
		t.Errorf("token should be absent when not provided: %q", frag.Get("token"))
	}
}

func TestBuildUIURL_DefaultPortOmitsFragment(t *testing.T) {
	// 18789 on localhost is the UI's implicit default — fragment is noise.
	got := buildUIURL("http://localhost:5173", "localhost", 18789, "", "main", "/chat")
	if strings.Contains(got, "#") {
		t.Errorf("URL should have no fragment when gatewayUrl matches UI default: %s", got)
	}
	if !strings.HasSuffix(got, "?session=main") {
		t.Errorf("session query missing: %s", got)
	}
}

func TestBuildUIURL_LoopbackAliasMatchesDefault(t *testing.T) {
	// 127.0.0.1 and localhost should be treated as equivalent.
	got := buildUIURL("http://127.0.0.1:5173", "localhost", 18789, "", "", "/chat")
	if strings.Contains(got, "#") {
		t.Errorf("loopback alias mismatch should not force a fragment: %s", got)
	}
}

func TestBuildUIURL_DefaultPortStillKeepsTokenFragment(t *testing.T) {
	// Token must always go in the fragment, even when the UI's default
	// would otherwise resolve.
	got := buildUIURL("http://localhost:5173", "localhost", 18789, "secret-tok", "main", "/chat")
	parsed, _ := url.Parse(got)
	if parsed.Fragment == "" {
		t.Fatalf("expected token fragment, got %s", got)
	}
	frag, _ := url.ParseQuery(parsed.Fragment)
	if frag.Get("token") != "secret-tok" {
		t.Errorf("token = %q", frag.Get("token"))
	}
	// gatewayUrl should NOT be redundantly included.
	if frag.Has("gatewayUrl") {
		t.Errorf("gatewayUrl should be omitted when port matches UI default: %s", got)
	}
}

func TestBuildUIURL_TokenAndSession(t *testing.T) {
	got := buildUIURL("http://example.test", "127.0.0.1", 9000, "secret-tok", "agent:main:main", "/chat")
	parsed, _ := url.Parse(got)
	// session in query, percent-encoded.
	if parsed.Query().Get("session") != "agent:main:main" {
		t.Errorf("session query = %q, want agent:main:main", parsed.Query().Get("session"))
	}
	frag, _ := url.ParseQuery(parsed.Fragment)
	if frag.Get("token") != "secret-tok" {
		t.Errorf("token fragment = %q, want secret-tok", frag.Get("token"))
	}
	if frag.Get("gatewayUrl") != "ws://127.0.0.1:9000" {
		t.Errorf("gatewayUrl fragment = %q", frag.Get("gatewayUrl"))
	}
	// Token must NOT appear in the query portion.
	if strings.Contains(parsed.RawQuery, "token") {
		t.Errorf("token leaked into query: %s", parsed.RawQuery)
	}
}

func TestBuildUIURL_NoSessionMeansNoQuery(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18790, "", "", "/chat")
	parsed, _ := url.Parse(got)
	if parsed.RawQuery != "" {
		t.Errorf("expected empty query when no session: %q", parsed.RawQuery)
	}
}

func TestBuildUIURL_CustomPath(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18790, "", "", "/agents")
	parsed, _ := url.Parse(got)
	if parsed.Path != "/agents" {
		t.Errorf("path = %q, want /agents", parsed.Path)
	}
}
