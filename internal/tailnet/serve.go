// Package tailnet brings up an embedded Tailscale node (tsnet) and advertises
// a pre-existing VIPService, handing the resulting listener to the gateway.
//
// This is the runtime half of ADR 0008's tailnet bind: the tsnet node *is*
// the gateway's network listener, so it runs in-process (not a gRPC plugin).
// Provisioning the service + tailnet objects is internal/tailscale's job;
// this package only advertises an existing service.
//
// The node registers from the OAuth ClientSecret + AdvertiseTags directly
// (per ADR 0008 Findings) — no separately-minted auth key. State persists
// under StateDir (default ~/.talon/tailscale) so subsequent boots reuse the
// same node identity.
package tailnet

import (
	"context"
	"fmt"
	"net"
	"strings"

	"tailscale.com/tsnet"
)

// Options configures the embedded tsnet node and the service it advertises.
type Options struct {
	Hostname      string   // tsnet node name, e.g. "talon"
	StateDir      string   // tsnet state dir, e.g. ~/.talon/tailscale
	ClientSecret  string   // OAuth client secret (tskey-client-...) for node registration
	AdvertiseTags []string // e.g. ["tag:talon"]
	Service       string   // svc-prefixed service name, e.g. "svc:talon"
	Port          int      // service port, e.g. 443
}

// Listener is the gateway-facing listener for the advertised service. It
// embeds the tsnet service listener and exposes its FQDN. Close tears down
// both the listener and the underlying tsnet node.
type Listener struct {
	net.Listener
	FQDN  string // e.g. talon.example.ts.net
	close func() error
}

// Close closes the listener and shuts the tsnet node down.
func (l *Listener) Close() error {
	if l.close != nil {
		return l.close()
	}
	return l.Listener.Close()
}

// Serve brings up a tsnet node and returns a listener advertising the
// service. Option validation happens before any tsnet construction so
// offline callers (and tests) get fast, deterministic errors.
func Serve(ctx context.Context, o Options) (*Listener, error) {
	if strings.TrimSpace(o.Service) == "" {
		return nil, fmt.Errorf("tailnet: service is required")
	}
	if !strings.HasPrefix(o.Service, "svc:") {
		return nil, fmt.Errorf("tailnet: service %q must have the svc: prefix (e.g. svc:talon)", o.Service)
	}
	if o.Port <= 0 {
		return nil, fmt.Errorf("tailnet: port must be > 0, got %d", o.Port)
	}

	srv := &tsnet.Server{
		Hostname:      o.Hostname,
		Dir:           o.StateDir,
		ClientSecret:  o.ClientSecret,
		AdvertiseTags: o.AdvertiseTags,
	}

	ln, err := srv.ListenService(o.Service, tsnet.ServiceModeHTTP{
		HTTPS: true,
		Port:  uint16(o.Port),
	})
	if err != nil {
		// Best-effort cleanup so a failed bring-up doesn't leak the node.
		_ = srv.Close()
		return nil, fmt.Errorf("tailnet: listen service %q: %w", o.Service, err)
	}

	return &Listener{
		Listener: ln,
		FQDN:     ln.FQDN,
		close: func() error {
			lerr := ln.Close()
			serr := srv.Close()
			if lerr != nil {
				return lerr
			}
			return serr
		},
	}, nil
}
