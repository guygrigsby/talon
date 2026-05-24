package main

import (
	"testing"

	"github.com/guygrigsby/talon/internal/server"
)

// TestPluginConstructors_AllRegisteredInBuiltins guards the wiring
// gap that just bit us with mac-open: a plugin can be added to
// pluginConstructors (so `talon plugin run <name>` works) but
// forgotten in internal/server.builtinPlugins (so the gateway's
// spec parser doesn't recognize it as first-party, falls through
// to legacy with no cmd, and load fails at runtime).
//
// Every name registered in pluginConstructors must round-trip
// through BuiltinPluginCmd to produce a non-empty spawn cmd.
func TestPluginConstructors_AllRegisteredInBuiltins(t *testing.T) {
	for name := range pluginConstructors {
		cmd := server.BuiltinPluginCmd(name)
		if len(cmd) == 0 {
			t.Errorf("plugin %q is in pluginConstructors but missing from internal/server.builtinPlugins — add an entry so the gateway can spawn it natively", name)
		}
	}
}
