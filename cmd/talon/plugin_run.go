package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/guygrigsby/talon/internal/plugin/native"
	"github.com/guygrigsby/talon/internal/plugin/pb"
	anthropicplug "github.com/guygrigsby/talon/internal/plugins/anthropic"
	"github.com/guygrigsby/talon/internal/plugins/bluebubbles"
	"github.com/guygrigsby/talon/internal/plugins/brave"
	"github.com/guygrigsby/talon/internal/plugins/macnotify"
	"github.com/guygrigsby/talon/internal/plugins/macopen"
	openaicompat "github.com/guygrigsby/talon/internal/plugins/openaicompat"
	"github.com/guygrigsby/talon/internal/plugins/telegram"
	"github.com/guygrigsby/talon/internal/plugins/whisper"
)

// pluginConstructors is the dispatch table for 'talon plugin run <name>'.
// Each entry's constructor is called to create the PluginServer; then
// pluginrun.Serve handles the gRPC lifecycle.
var pluginConstructors = map[string]func() (pb.PluginServer, error){
	"anthropic":     anthropicplug.New,
	"openai-compat": openaicompat.New,
	"telegram":      telegram.New,
	"brave":         brave.New,
	"whisper":       whisper.New,
	"bluebubbles":   bluebubbles.New,
	"mac-notify":    macnotify.New,
	"mac-open":      macopen.New,
}

func pluginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "plugin",
		Short: "First-party plugin management",
	}
	c.AddCommand(pluginRunCmd())
	return c
}

func pluginRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <name>",
		Short: "Run a first-party plugin as a gateway subprocess",
		Long: `Run a bundled plugin in-process. The host spawns this via
plugins.entries.<name>.cmd = ["talon", "plugin", "run", "<name>"].
Not intended for direct human invocation — the host sets the required
TALON_PLUGIN_HANDSHAKE and TALON_PLUGIN_AUTH_COOKIE env vars.`,
		Args:             cobra.ExactArgs(1),
		ValidArgs:        pluginNames(),
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctor, ok := pluginConstructors[name]
			if !ok {
				return fmt.Errorf("unknown plugin %q (known: %v)", name, pluginNames())
			}
			srv, err := ctor()
			if err != nil {
				return fmt.Errorf("plugin %s: init failed: %w", name, err)
			}
			native.Serve(name, srv)
			return nil // unreachable; Serve never returns
		},
	}
}

func pluginNames() []string {
	names := make([]string, 0, len(pluginConstructors))
	for n := range pluginConstructors {
		names = append(names, n)
	}
	return names
}
