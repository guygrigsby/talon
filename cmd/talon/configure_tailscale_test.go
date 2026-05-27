package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/tidwall/gjson"
)

// fakeProvisioner is a scripted tailscaleProvisioner for the wizard tests.
type fakeProvisioner struct {
	tailnet      string
	createdName  string
	createdPorts []string
}

func (f *fakeProvisioner) TailnetName(context.Context) (string, error) {
	return f.tailnet, nil
}

func (f *fakeProvisioner) CreateService(_ context.Context, name string, ports []string) error {
	f.createdName = name
	f.createdPorts = ports
	return nil
}

func TestConfigureTailscale_WritesRefsAndBind(t *testing.T) {
	// Isolate state under a temp dir so config writes don't touch the
	// real ~/.talon.
	stateDir := t.TempDir()
	t.Setenv("TALON_STATE_DIR", stateDir)
	t.Setenv("TS_OAUTH_CLIENT_ID", "")
	t.Setenv("TS_OAUTH_CLIENT_SECRET", "")

	fake := &fakeProvisioner{tailnet: "example.ts.net"}
	oldNew := newTailscaleClient
	newTailscaleClient = func(_ context.Context, id, secret string) (tailscaleProvisioner, error) {
		if id != "client-id-123" {
			t.Fatalf("client id = %q", id)
		}
		if secret != "client-secret-xyz" {
			t.Fatalf("client secret = %q", secret)
		}
		return fake, nil
	}
	defer func() { newTailscaleClient = oldNew }()

	var storedTarget, storedSecret string
	oldStore := storeTailscaleSecret
	storeTailscaleSecret = func(_ context.Context, target, secret string) (string, error) {
		storedTarget = target
		storedSecret = secret
		return "keychain://" + target, nil
	}
	defer func() { storeTailscaleSecret = oldStore }()

	// Scripted stdin: client id, client secret, service label (default
	// talon -> blank), port (default 443 -> blank), ACL apply prompt
	// (default N -> blank).
	in := strings.NewReader("client-id-123\nclient-secret-xyz\n\n\n\n")
	out := &bytes.Buffer{}
	if err := configureTailscale(in, out); err != nil {
		t.Fatalf("configureTailscale: %v", err)
	}

	got := out.String()

	// Secret stored as a ref; plaintext secret must never be printed.
	if storedSecret != "client-secret-xyz" {
		t.Fatalf("stored secret = %q", storedSecret)
	}
	if !strings.Contains(storedTarget, "tailscale") {
		t.Fatalf("store target = %q", storedTarget)
	}
	if strings.Contains(got, "client-secret-xyz") {
		t.Fatal("plaintext OAuth secret leaked into wizard output")
	}

	// Service created with the svc-prefixed name + default port.
	if fake.createdName != "svc:talon" {
		t.Fatalf("created service = %q", fake.createdName)
	}
	if len(fake.createdPorts) != 1 || fake.createdPorts[0] != "443" {
		t.Fatalf("created ports = %v", fake.createdPorts)
	}

	// Output surfaces the tailnet URL + ACL grant snippet.
	if !strings.Contains(got, "talon.example.ts.net") {
		t.Fatalf("output missing FQDN URL: %q", got)
	}
	if !strings.Contains(got, "svc:talon") || !strings.Contains(got, "autoApprovers") {
		t.Fatalf("output missing ACL grant snippet: %q", got)
	}

	// Config written: ref (not plaintext), bind=tailnet, service, etc.
	cfgPath := filepath.Join(stateDir, "config.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	// config.toml is TOML; re-read via the runtime JSON view by checking
	// raw text for the secret never appearing in plaintext.
	if strings.Contains(string(raw), "client-secret-xyz") {
		t.Fatal("plaintext OAuth secret written to config.toml")
	}

	merged, err := config.MergedBytes(resolvePaths())
	if err != nil {
		t.Fatalf("merged config: %v", err)
	}
	if v := gjson.GetBytes(merged, "gateway.bind").Str; v != "tailnet" {
		t.Fatalf("gateway.bind = %q, want tailnet", v)
	}
	if v := gjson.GetBytes(merged, "gateway.tailscale.service").Str; v != "svc:talon" {
		t.Fatalf("gateway.tailscale.service = %q", v)
	}
	if v := gjson.GetBytes(merged, "gateway.tailscale.oauth_client_id").Str; v != "client-id-123" {
		t.Fatalf("gateway.tailscale.oauth_client_id = %q", v)
	}
	ref := gjson.GetBytes(merged, "gateway.tailscale.oauth_client_ref").Str
	if !strings.HasPrefix(ref, "keychain://") {
		t.Fatalf("oauth_client_ref = %q, want a keychain:// ref", ref)
	}
	if v := gjson.GetBytes(merged, "gateway.tailscale.tailnet").Str; v != "example.ts.net" {
		t.Fatalf("gateway.tailscale.tailnet = %q", v)
	}
	if v := gjson.GetBytes(merged, "gateway.port").Int(); v != 443 {
		t.Fatalf("gateway.port = %d", v)
	}
}
