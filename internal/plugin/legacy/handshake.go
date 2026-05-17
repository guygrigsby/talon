// Package plugin runs talon's gRPC plugin host: spawns plugin
// subprocesses, performs the handshake, dials their Plugin service, and
// gates Host-service callbacks against per-plugin capability manifests.
//
// Handshake protocol (modeled on Hashicorp go-plugin, simplified):
//
//  1. Host generates a 24-byte random auth cookie.
//  2. Host spawns the plugin binary with three env vars:
//
//       TALON_PLUGIN_HANDSHAKE   = handshakeMagic ("talon.plugin.v1")
//       TALON_PLUGIN_AUTH_COOKIE = <hex cookie>
//       TALON_PLUGIN_HOST_ADDR   = <host-service-address>
//
//  3. Plugin starts its gRPC server on a free port and prints ONE line
//     to stdout in the form:
//
//       1|TCP|127.0.0.1:54321|grpc
//
//  4. Host parses that line, dials the address, calls Plugin.Initialize
//     with the auth cookie + host address.
//  5. Plugin returns its Manifest.
//  6. For every Host-service call back from the plugin, the plugin
//     attaches the auth cookie in gRPC metadata under the
//     CookieMetadataKey header. The host's capability interceptor
//     resolves cookie → plugin → manifest → capability check.
//
// Subsequent stdout lines are forwarded to talon's logs as
// "[plugin/<name>] <line>" so plugin authors can debug-print.
package legacy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	// HandshakeVersion is the wire-protocol version of the stdout
	// handshake line. Bumped only on incompatible changes — additive
	// fields can ride along the gRPC service.
	HandshakeVersion = 1

	// HandshakeMagic is set in EnvHandshake so a plugin binary executed
	// outside talon can detect it's not in a real plugin context and
	// fail loudly instead of acting unpredictably.
	HandshakeMagic = "talon.plugin.v1"

	// EnvHandshake is the env var the host sets when launching a
	// plugin. Plugins should refuse to start if this isn't HandshakeMagic.
	EnvHandshake = "TALON_PLUGIN_HANDSHAKE"

	// EnvAuthCookie is the per-launch random secret the plugin echoes
	// back in gRPC metadata on every Host-service call.
	EnvAuthCookie = "TALON_PLUGIN_AUTH_COOKIE"

	// EnvHostAddr is the address (host:port) the plugin dials when it
	// wants to call the Host service.
	EnvHostAddr = "TALON_PLUGIN_HOST_ADDR"

	// CookieMetadataKey is the gRPC metadata header plugins use to
	// authenticate Host-service calls. Lowercase per gRPC convention
	// (Go gRPC normalizes anyway, but keep it canonical).
	CookieMetadataKey = "talon-plugin-auth-cookie"
)

// HandshakeLine is the parsed form of the plugin's first stdout line.
type HandshakeLine struct {
	Version  int
	Network  string // "TCP" today; reserved for "UNIX" later
	Address  string // dial target — e.g. "127.0.0.1:54321"
	Protocol string // "grpc"
}

// ParseHandshakeLine parses a plugin's handshake line. The format is
// fixed: "<version>|<network>|<address>|<protocol>".
func ParseHandshakeLine(s string) (*HandshakeLine, error) {
	parts := strings.Split(strings.TrimSpace(s), "|")
	if len(parts) != 4 {
		return nil, fmt.Errorf("handshake: expected 4 fields, got %d (%q)", len(parts), s)
	}
	v, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("handshake: invalid version %q: %w", parts[0], err)
	}
	if v != HandshakeVersion {
		return nil, fmt.Errorf("handshake: unsupported version %d (want %d)", v, HandshakeVersion)
	}
	if parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("handshake: empty network or address")
	}
	if parts[3] != "grpc" {
		return nil, fmt.Errorf("handshake: unsupported protocol %q (want grpc)", parts[3])
	}
	return &HandshakeLine{
		Version:  v,
		Network:  parts[1],
		Address:  parts[2],
		Protocol: parts[3],
	}, nil
}

// generateAuthCookie returns a random 48-char hex string. 192 bits is
// plenty for a per-launch secret.
func generateAuthCookie() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("handshake: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
