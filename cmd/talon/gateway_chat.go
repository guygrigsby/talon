package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/plugin"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/provider/deepseek"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/tools"
	"github.com/tidwall/gjson"
)

// configAgentResolver implements server.AgentResolver by reading talon's
// merged config on each call. Cheap enough that we don't cache: the merged
// view is parsed in-memory from already-cached overlay bytes.
//
// Model resolution mirrors openclaw's precedence:
//
//  1. per-agent agents.list[id==X].model.primary (object form)
//  2. per-agent agents.list[id==X].model         (string shorthand)
//  3. agents.defaults.model.primary              (global default)
//
// First non-empty hit wins. If the agent isn't in agents.list at all,
// returns ErrAgentNotFound.
type configAgentResolver struct {
	paths openclaw.Paths
}

// Workspace implements server.WorkspaceResolver: per-agent workspace,
// fallback to agents.defaults.workspace.
func (r *configAgentResolver) Workspace(agentID string) (string, error) {
	merged, err := config.MergedBytes(r.paths)
	if err != nil {
		return "", fmt.Errorf("read merged config: %w", err)
	}
	agent := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID))
	if !agent.Exists() {
		return "", fmt.Errorf("%w: %q", server.ErrAgentNotFound, agentID)
	}
	if v := agent.Get("workspace"); v.Exists() && v.Str != "" {
		return v.Str, nil
	}
	if v := gjson.GetBytes(merged, "agents.defaults.workspace"); v.Exists() && v.Str != "" {
		return v.Str, nil
	}
	return "", fmt.Errorf("agent %q has no workspace and no agents.defaults.workspace", agentID)
}

// newToolRunnerFactory returns a ToolRunnerFor closure that wraps the
// per-workspace base runner (builtins + subagent) in a plugin.ToolRouter
// when host is non-nil. With host == nil it degrades to the base — same
// behavior as before plugins existed, kept so test paths and
// no-plugin gateways stay simple.
//
// host is captured by reference; new plugins loaded after this factory
// is constructed light up automatically because ToolRouter walks
// host.List() per Specs/Run call.
func newToolRunnerFactory(host *plugin.Host) func(workspace string, sub server.SubagentRunner) server.ToolRunner {
	return func(workspace string, sub server.SubagentRunner) server.ToolRunner {
		base := tools.NewWithSubagent(workspace, sub)
		if host == nil {
			return base
		}
		return plugin.NewToolRouter(base, host)
	}
}

// pluginSpec is one parsed plugins.entries.<name> entry that talon
// should spawn (enabled=true with a non-empty cmd array).
type pluginSpec struct {
	name string
	cmd  []string
}

// parsePluginSpecs walks plugins.entries.<name> in the merged config
// JSON and returns the entries talon should LoadPlugin. Entries
// without cmd (the openclaw-style enabled flags for native built-in
// extensions) are silently skipped — those are managed by the runtime
// they're shipped in, not by this loader. Pure function for testability.
func parsePluginSpecs(merged []byte) []pluginSpec {
	var specs []pluginSpec
	gjson.GetBytes(merged, "plugins.entries").ForEach(func(nameKey, entry gjson.Result) bool {
		if !entry.Get("enabled").Bool() {
			return true
		}
		cmdResult := entry.Get("cmd")
		if !cmdResult.IsArray() {
			return true
		}
		var cmd []string
		cmdResult.ForEach(func(_, v gjson.Result) bool {
			if v.Type == gjson.String && v.Str != "" {
				cmd = append(cmd, v.Str)
			}
			return true
		})
		if len(cmd) == 0 {
			return true
		}
		specs = append(specs, pluginSpec{name: nameKey.Str, cmd: cmd})
		return true
	})
	return specs
}

// loadConfiguredPlugins reads the merged config, parses the spawn-able
// plugin entries, and asks host to LoadPlugin each one. Each load logs
// one line. Failures don't abort the gateway — they just leave the
// plugin un-registered (a broken plugin shouldn't take down chat).
func loadConfiguredPlugins(ctx context.Context, host *plugin.Host, paths openclaw.Paths) {
	merged, err := config.MergedBytes(paths)
	if err != nil {
		log.Printf("plugins: read merged config: %v", err)
		return
	}
	for _, spec := range parsePluginSpecs(merged) {
		inst, err := host.LoadPlugin(ctx, spec.name, plugin.LoadOptions{Cmd: spec.cmd})
		if err != nil {
			log.Printf("plugin %s: load failed: %v", spec.name, err)
			continue
		}
		log.Printf("plugin %s: loaded — tools=%d providers=%d channels=%d",
			spec.name,
			len(inst.Manifest.GetOffersTools()),
			len(inst.Manifest.GetOffersProviders()),
			len(inst.Manifest.GetOffersChannels()),
		)
	}
}

func (r *configAgentResolver) PrimaryModel(agentID string) (provider.ModelID, error) {
	merged, err := config.MergedBytes(r.paths)
	if err != nil {
		return "", fmt.Errorf("read merged config: %w", err)
	}
	agent := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID))
	if !agent.Exists() {
		return "", fmt.Errorf("%w: %q", server.ErrAgentNotFound, agentID)
	}
	// Per-agent model: object form first, then string shorthand.
	if v := agent.Get("model.primary"); v.Exists() && v.Str != "" {
		return provider.ModelID(v.Str), nil
	}
	if v := agent.Get("model"); v.Exists() && v.Type == gjson.String && v.Str != "" {
		return provider.ModelID(v.Str), nil
	}
	// Global default.
	if v := gjson.GetBytes(merged, "agents.defaults.model.primary"); v.Exists() && v.Str != "" {
		return provider.ModelID(v.Str), nil
	}
	return "", fmt.Errorf("agent %q has no resolvable model (no per-agent model and no agents.defaults.model.primary)", agentID)
}

// agentProviderFactory implements server.ProviderFactory. Resolution
// order: native built-ins (openai, deepseek) first, then any loaded
// plugin whose manifest offers the requested provider name. Only fails
// with ErrProviderUnavailable when neither side recognizes the name —
// that's the signal the model targeted a provider nothing can serve.
type agentProviderFactory struct {
	paths openclaw.Paths
	host  *plugin.Host // optional; nil disables plugin-served providers
}

func (f *agentProviderFactory) For(providerName, agentID string) (provider.Provider, error) {
	authPath := filepath.Join(f.paths.Openclaw.AgentDir(agentID), "agent", "auth-profiles.json")
	switch providerName {
	case "openai":
		key, err := openai.LoadAPIKey(authPath, "openai:default")
		if err != nil {
			return nil, fmt.Errorf("openai: %w", err)
		}
		return openai.New(openai.Options{APIKey: key}), nil
	case "deepseek":
		key, err := deepseek.LoadAPIKey(authPath)
		if err != nil {
			return nil, fmt.Errorf("deepseek: %w", err)
		}
		return deepseek.New(deepseek.Options{APIKey: key}), nil
	}
	// No native match — try a plugin offering this provider.
	if f.host != nil {
		if inst := f.host.ProviderByName(providerName); inst != nil {
			return plugin.NewPluginProvider(providerName, inst.Client), nil
		}
	}
	return nil, fmt.Errorf("%w: %q (implemented natively: openai, deepseek; no loaded plugin offers it)",
		server.ErrProviderUnavailable, providerName)
}
