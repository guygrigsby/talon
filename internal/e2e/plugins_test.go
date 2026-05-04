//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestE2E_PluginChannelRoundtrip is the load-bearing test for the
// channel-plugin path. It boots talon-gateway in a container, loads
// the bundled testplugin (which advertises both the testchan channel
// and a testprov/echo-1 model), and verifies the full lifecycle:
//
//	plugin StartChannel → gateway dispatcher → ChatHandler.RunForSession
//	→ testprov StreamCompletion → SendChannelMessage back through plugin
//
// The testplugin's StartChannel emits exactly two pre-canned inbound
// messages then closes; the dispatcher routes each through the agent
// "main" (configured to use testprov/echo-1, which the same plugin
// serves), and posts the assistant text back via SendChannelMessage.
// The plugin's stderr logs each SendChannelMessage call, which is
// what we assert on.
//
// This is the harness validation test — every production plugin path
// (Go subprocess, future Node.js compat shim) goes through the same
// boot/handshake/manifest/RPC flow exercised here.
func TestE2E_PluginChannelRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	cfg := []byte(`{
		"agents": {
			"list": [
				{"id": "main", "model": "testprov/echo-1", "workspace": "/tmp/ws"}
			]
		},
		"plugins": {
			"entries": {
				"testplugin": {
					"enabled": true,
					"cmd": ["/usr/local/bin/talon-testplugin"]
				}
			}
		},
		"channels": {
			"testchan": {"agentId": "main"}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	// The testplugin emits two messages: one room-scoped ("hello room"
	// → room-1), one direct ("hi direct" → user-B). The dispatcher's
	// SessionRunner returns "echo: <text>" via testprov, so we expect
	// two SendChannelMessage stderr lines, one per inbound.
	if _, err := g.WaitForLog(`text="echo: hello room"`, 30*time.Second); err != nil {
		t.Errorf("did not see room-scoped reply: %v\n--- container logs ---\n%s", err, g.LogsString())
	}
	if _, err := g.WaitForLog(`text="echo: hi direct"`, 30*time.Second); err != nil {
		t.Errorf("did not see direct-scoped reply: %v\n--- container logs ---\n%s", err, g.LogsString())
	}

	// The gateway also logs that it bound the channel — sanity-check
	// the wiring announce so a regression that loaded the plugin but
	// failed to dispatch shows up specifically.
	logs := g.LogsString()
	if !strings.Contains(logs, "channel dispatching") || !strings.Contains(logs, "channel=testchan") || !strings.Contains(logs, "agent=main") {
		t.Errorf("expected dispatcher startup log; got logs:\n%s", logs)
	}
}

// TestE2E_PluginLoadsAndAnnounces verifies the simpler invariant that
// a bare gateway boot with one plugin enabled in config actually
// spawns the plugin and announces its manifest (tools=1, providers=1,
// channels=1 for the testplugin). Doesn't depend on the channel
// dispatcher firing — runs faster and provides earlier signal when
// something breaks the spawn/handshake path.
func TestE2E_PluginLoadsAndAnnounces(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	cfg := []byte(`{
		"agents": {"list": [{"id": "main", "model": "testprov/echo-1", "workspace": "/tmp/ws"}]},
		"plugins": {
			"entries": {
				"testplugin": {"enabled": true, "cmd": ["/usr/local/bin/talon-testplugin"]}
			}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	line, err := g.WaitForLog(`plugin loaded plugin=testplugin`, 30*time.Second)
	if err != nil {
		t.Fatalf("did not see plugin load announce: %v\n--- container logs ---\n%s", err, g.LogsString())
	}
	// Announce format: 'INFO  plugin loaded plugin=testplugin tools=1 providers=1 channels=1'
	if !strings.Contains(line, "tools=1") || !strings.Contains(line, "providers=1") || !strings.Contains(line, "channels=1") {
		t.Errorf("manifest counts wrong: %q", line)
	}
}
