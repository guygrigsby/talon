package main

import (
	"context"
	"net"
	"testing"

	"github.com/guygrigsby/talon/internal/tailnet"
)

func TestBindTailnetUsesTailnetServe(t *testing.T) {
	called := false
	old := tailnetServe
	tailnetServe = func(ctx context.Context, o tailnet.Options) (*tailnet.Listener, error) {
		called = true
		if o.Service != "svc:talon" {
			t.Fatalf("service = %q, want svc:talon", o.Service)
		}
		if o.Port != 8443 {
			t.Fatalf("port = %d, want 8443", o.Port)
		}
		// A scheme-less ref resolves to itself via the secrets resolver,
		// so the OAuth client secret reaches tsnet unchanged.
		if o.ClientSecret != "client-secret-value" {
			t.Fatalf("client secret = %q, want resolved secret", o.ClientSecret)
		}
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		return &tailnet.Listener{Listener: l, FQDN: "talon.example.ts.net"}, nil
	}
	defer func() { tailnetServe = old }()

	merged := []byte(`{
		"gateway": {
			"bind": "tailnet",
			"port": 8443,
			"tailscale": {
				"service": "svc:talon",
				"oauth_client_ref": "client-secret-value"
			}
		}
	}`)

	ln, fqdn, err := gatewayTailnetListener(context.Background(), merged)
	if err != nil {
		t.Fatalf("gatewayTailnetListener: %v", err)
	}
	defer ln.Close()
	if !called {
		t.Fatal("tailnetServe was not called")
	}
	if fqdn != "talon.example.ts.net" {
		t.Fatalf("fqdn = %q", fqdn)
	}
}

func TestGatewayTailnetListenerRequiresService(t *testing.T) {
	merged := []byte(`{"gateway":{"bind":"tailnet","port":8443}}`)
	if _, _, err := gatewayTailnetListener(context.Background(), merged); err == nil {
		t.Fatal("want error when gateway.tailscale.service is unset")
	}
}
