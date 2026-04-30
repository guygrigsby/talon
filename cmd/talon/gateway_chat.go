package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/netutil"
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

// ToolsEnabled implements server.AgentToolsResolver. Reads
// `agents.list[id==X].tools.enabled` from the merged config; default
// is true (existing agents continue advertising tools to the model).
// Set to false on a chat-only agent to suppress tool spec generation
// + dispatch entirely — useful for personas pinned to local models
// that don't support function calling.
func (r *configAgentResolver) ToolsEnabled(agentID string) (bool, error) {
	merged, err := config.MergedBytes(r.paths)
	if err != nil {
		return true, fmt.Errorf("read merged config: %w", err)
	}
	v := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q).tools.enabled`, agentID))
	if !v.Exists() {
		return true, nil
	}
	return v.Bool(), nil
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
// per-workspace base runner (builtins + subagent + merged-agents) in a
// plugin.ToolRouter when host is non-nil. With host == nil it degrades
// to the base — same behavior as before plugins existed, kept so test
// paths and no-plugin gateways stay simple.
//
// paths is threaded so the `agents` tool can resolve the layered
// overlay; without it agents only see ~/.openclaw/openclaw.json via
// the read tool and miss talon-overlay-only entries (e.g. a "chat"
// persona defined under ~/.talon).
//
// host is captured by reference; new plugins loaded after this factory
// is constructed light up automatically because ToolRouter walks
// host.List() per Specs/Run call.
func newToolRunnerFactory(host *plugin.Host, paths openclaw.Paths) func(workspace string, sub server.SubagentRunner) server.ToolRunner {
	return func(workspace string, sub server.SubagentRunner) server.ToolRunner {
		base := tools.NewWithSubagentAndPaths(workspace, sub, paths)
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

// defaultPluginDefaults probes the runtime for sensible bundled-plugin
// defaults so users don't have to set plugins.bundled.path on a
// standard Docker install. Resolution order:
//
//  1. TALON_EXTENSIONS_PATH env var (explicit, highest priority).
//  2. /opt/extensions if it exists (the path the Dockerfile bakes in).
//
// Both yield empty when nothing's set; the merged config can still
// supply plugins.bundled.path explicitly, in which case neither
// default applies.
func defaultPluginDefaults() pluginParseDefaults {
	if v := os.Getenv("TALON_EXTENSIONS_PATH"); v != "" {
		return pluginParseDefaults{bundledPath: v}
	}
	if _, err := os.Stat("/opt/extensions"); err == nil {
		return pluginParseDefaults{bundledPath: "/opt/extensions"}
	}
	return pluginParseDefaults{}
}

// pluginParseDefaults provides fallback values for the bundled-extension
// shortcut when the merged config doesn't set them. Production callers
// fill bundledPath from the runtime environment (TALON_EXTENSIONS_PATH
// or a probe of /opt/extensions); tests can pass zero values.
type pluginParseDefaults struct {
	bundledPath string
	shimCmd     []string
}

// parsePluginSpecs walks plugins.entries.<name> in the merged config
// and returns the entries talon should LoadPlugin.
//
// Two ways an entry produces a spawn cmd:
//
//  1. Explicit cmd: `cmd: ["/path/to/binary", ...]`. Used as-is.
//  2. Bundled openclaw extension: `bundled: "anthropic"`. Resolves to
//     `<shimCmd...> <plugins.bundled.path>/anthropic`. The shim is the
//     Node-side openclaw-plugin-host that bridges the extension's
//     register*() hooks to talon's gRPC plugin protocol.
//
// Entries without either are silently skipped — openclaw-style
// enabled flags for native runtime built-ins, not subprocesses we own.
// Pure function for testability.
func parsePluginSpecs(merged []byte, defaults pluginParseDefaults) []pluginSpec {
	bundledPath := gjson.GetBytes(merged, "plugins.bundled.path").Str
	if bundledPath == "" {
		bundledPath = defaults.bundledPath
	}
	shimCmd := stringArray(gjson.GetBytes(merged, "plugins.bundled.shimCmd"))
	if len(shimCmd) == 0 {
		shimCmd = defaults.shimCmd
	}
	if len(shimCmd) == 0 {
		// Default shim invocation: rely on PATH lookup of node and the
		// openclaw-plugin-host symlink. The Dockerfile sets both up;
		// host installs need Node + the shim on PATH.
		shimCmd = []string{"node", "openclaw-plugin-host"}
	}

	var specs []pluginSpec
	gjson.GetBytes(merged, "plugins.entries").ForEach(func(nameKey, entry gjson.Result) bool {
		if !entry.Get("enabled").Bool() {
			return true
		}
		// Explicit cmd wins.
		if cmd := stringArray(entry.Get("cmd")); len(cmd) > 0 {
			specs = append(specs, pluginSpec{name: nameKey.Str, cmd: cmd})
			return true
		}
		// Bundled-extension shortcut. Skip silently when bundled.path
		// isn't configured (the user enabled a bundled extension but
		// didn't tell us where they live — log so they can spot it).
		if extName := entry.Get("bundled").Str; extName != "" {
			if bundledPath == "" {
				log.Printf("plugin %s: bundled=%q but plugins.bundled.path is unset; skipping", nameKey.Str, extName)
				return true
			}
			fullCmd := append(append([]string(nil), shimCmd...), filepath.Join(bundledPath, extName))
			specs = append(specs, pluginSpec{name: nameKey.Str, cmd: fullCmd})
			return true
		}
		return true
	})
	return specs
}

// stringArray reads a JSON array of strings into a Go slice, skipping
// non-string entries. Callers treat an empty result as "absent" rather
// than "explicit empty array" — both shapes mean the same thing here.
func stringArray(v gjson.Result) []string {
	if !v.IsArray() {
		return nil
	}
	var out []string
	v.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String && item.Str != "" {
			out = append(out, item.Str)
		}
		return true
	})
	return out
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
	for _, spec := range parsePluginSpecs(merged, defaultPluginDefaults()) {
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

// pluginChannelOffer is one (plugin name, channel name) pair drawn
// from a manifest. Lets parseChannelBindings stay a pure function over
// data instead of having to read host.List() / host.Get().
type pluginChannelOffer struct {
	PluginName string
	Channel    string
}

// channelBindingForPlugin pairs a plugin name with a ChannelBinding —
// the plugin name is needed to look up the Instance the dispatcher
// dials.
type channelBindingForPlugin struct {
	PluginName string
	Binding    plugin.ChannelBinding
}

// parseChannelBindings reads each offered channel against the merged
// config's channels.<name> sub-tree. Channels with no config tree are
// silently skipped (declaring a channel offer in a manifest is not the
// same as enabling it). agentId is read from channels.<name>.agentId
// with "main" as the default so a plain `{ "telegram": { "botToken":
// "..." } }` "just works".
//
// Pure function for testability; lifecycle wiring (host lookups,
// dispatcher start/stop) lives in startConfiguredChannels.
func parseChannelBindings(merged []byte, offers []pluginChannelOffer) []channelBindingForPlugin {
	var out []channelBindingForPlugin
	for _, off := range offers {
		cfg := gjson.GetBytes(merged, fmt.Sprintf("channels.%s", off.Channel))
		if !cfg.Exists() {
			continue
		}
		agentID := cfg.Get("agentId").Str
		if agentID == "" {
			agentID = "main"
		}
		out = append(out, channelBindingForPlugin{
			PluginName: off.PluginName,
			Binding: plugin.ChannelBinding{
				ChannelName: off.Channel,
				AgentID:     agentID,
				ConfigJSON:  []byte(cfg.Raw),
			},
		})
	}
	return out
}

// collectChannelOffers walks every loaded plugin's manifest and returns
// each offered channel as a (plugin name, channel) pair. Pure data —
// kept separate from parseChannelBindings so the parsing rule can be
// tested without spinning up a real Host.
func collectChannelOffers(host *plugin.Host) []pluginChannelOffer {
	var out []pluginChannelOffer
	for _, name := range host.List() {
		inst := host.Get(name)
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, channel := range inst.Manifest.GetOffersChannels() {
			out = append(out, pluginChannelOffer{PluginName: name, Channel: channel})
		}
	}
	return out
}

// startConfiguredChannels wires channel dispatchers for every loaded
// plugin that offers a channel referenced in the merged config. Returns
// the dispatchers so the caller can Stop() them on shutdown. Failures
// log but don't propagate — a broken channel must not take down chat.
func startConfiguredChannels(ctx context.Context, host *plugin.Host, paths openclaw.Paths, runner plugin.SessionRunner) []*plugin.ChannelDispatcher {
	merged, err := config.MergedBytes(paths)
	if err != nil {
		log.Printf("channels: read merged config: %v", err)
		return nil
	}
	bindings := parseChannelBindings(merged, collectChannelOffers(host))
	out := make([]*plugin.ChannelDispatcher, 0, len(bindings))
	for _, b := range bindings {
		inst := host.Get(b.PluginName)
		if inst == nil {
			continue
		}
		d, err := plugin.NewChannelDispatcher(inst, b.Binding, runner)
		if err != nil {
			log.Printf("channel %s/%s: %v", b.PluginName, b.Binding.ChannelName, err)
			continue
		}
		d.Start(ctx)
		out = append(out, d)
		log.Printf("plugin %s: channel %q dispatching to agent %q",
			b.PluginName, b.Binding.ChannelName, b.Binding.AgentID)
	}
	return out
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

// authProfilePath returns the auth-profiles.json path for agentID using
// the standard talon layering: talon overlay first (so a talon-only agent
// like "chat" can carry its own keys under ~/.talon/agents/<id>/), then
// openclaw layer. The first path that exists wins; if neither exists we
// return the openclaw path so the existing not-found error shape is
// preserved for callers like LoadAPIKey that require a real file.
func (f *agentProviderFactory) authProfilePath(agentID string) string {
	talonAuth := filepath.Join(f.paths.Talon.AgentDir(agentID), "agent", "auth-profiles.json")
	if _, err := os.Stat(talonAuth); err == nil {
		return talonAuth
	}
	return filepath.Join(f.paths.Openclaw.AgentDir(agentID), "agent", "auth-profiles.json")
}

func (f *agentProviderFactory) For(providerName, agentID string) (provider.Provider, error) {
	authPath := f.authProfilePath(agentID)
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
	case "lmstudio":
		// Local LM Studio (or any OpenAI-compatible local server).
		// Auth is OPTIONAL for unauthenticated local installs but
		// REQUIRED when LM Studio is configured to enforce tokens
		// (newer versions reject placeholder strings as "malformed").
		//
		// Resolution order:
		//   1. per-agent agents/<id>/agent/auth-profiles.json
		//   2. main agent's profile (LM Studio is a gateway-shared
		//      local resource — see lmstudio_discovery.go for the
		//      same fallback used by models.list)
		//   3. placeholder "lm-studio" — works for unauthenticated
		//      installs; LM Studio servers with auth enforced will
		//      reject it with HTTP 401.
		//
		// Base URL is overrideable via models.providers.lmstudio.baseUrl
		// so non-default ports and remote LAN servers work without
		// code changes.
		key, err := openai.LoadProfileKeyOptional(authPath, "lmstudio:default", "lmstudio")
		if err != nil {
			return nil, fmt.Errorf("lmstudio: %w", err)
		}
		if key == "" && agentID != "main" {
			mainAuth := f.authProfilePath("main")
			if k, err := openai.LoadProfileKeyOptional(mainAuth, "lmstudio:default", "lmstudio"); err == nil && k != "" {
				key = k
			}
		}
		if key == "" {
			key = "lm-studio" // placeholder — non-empty so the openai package's APIKey gate passes
		}
		baseURL := f.lookupLMStudioBaseURL()
		return openai.New(openai.Options{
			APIKey:      key,
			BaseURL:     baseURL,
			Name:        "lmstudio",
			ProviderKey: "lmstudio",
		}), nil
	}
	// No native match — try a plugin offering this provider.
	if f.host != nil {
		if inst := f.host.ProviderByName(providerName); inst != nil {
			return plugin.NewPluginProvider(providerName, inst.Client), nil
		}
	}
	return nil, fmt.Errorf("%w: %q (implemented natively: openai, deepseek, lmstudio; no loaded plugin offers it)",
		server.ErrProviderUnavailable, providerName)
}

// lookupLMStudioBaseURL returns the LM Studio base URL from the
// merged config, defaulting to LM Studio's native REST endpoint.
// Looked up per-call so a config edit takes effect on the next
// chat.send without restart.
//
// We default to /api/v0 (LM Studio's native API) instead of /v1
// (OpenAI-compat shim). Both speak the OpenAI request/response
// shape for chat, but /api/v0/models returns richer metadata
// (architecture, quantization, loaded state, max_context_length)
// — useful in the picker, and the only path discovery can
// reliably parse to filter unloaded models. Users running an
// older LM Studio that only exposes /v1 can override via
// models.providers.lmstudio.baseUrl.
//
// When the gateway is running inside a container, "localhost" /
// "127.0.0.1" inside the URL refers to the container — but the
// user almost certainly meant their host machine, where LM Studio
// is running. We rewrite the host segment to "host.docker.internal"
// so LM Studio Just Works without per-host configuration. The
// rewrite only fires when (1) we're in a container AND (2) the URL
// targets a loopback host. Real LAN/remote URLs pass through.
func (f *agentProviderFactory) lookupLMStudioBaseURL() string {
	const defaultURL = "http://localhost:1234/api/v0"
	raw := defaultURL
	if merged, err := config.MergedBytes(f.paths); err == nil {
		if v := gjson.GetBytes(merged, "models.providers.lmstudio.baseUrl"); v.Exists() && v.Str != "" {
			raw = v.Str
		}
	}
	return netutil.RewriteLoopbackForContainer(raw)
}
