package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sync"

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
	case "lmstudio":
		// Local LM Studio (or any OpenAI-compatible local server).
		// Auth is OPTIONAL: most installs run unauthenticated on
		// loopback. If the user has set up an "lmstudio:default"
		// profile in auth-profiles.json (e.g. they're proxying LM
		// Studio behind nginx with a token), we use that key;
		// otherwise we send a placeholder the local server ignores.
		// Base URL is overrideable via
		// models.providers.lmstudio.baseUrl so non-default ports and
		// remote LAN servers work without code changes.
		key, err := openai.LoadProfileKeyOptional(authPath, "lmstudio:default", "lmstudio")
		if err != nil {
			return nil, fmt.Errorf("lmstudio: %w", err)
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

// lookupLMStudioBaseURL returns the LM Studio OpenAI-compatible base
// URL from the merged config, defaulting to the upstream's standard
// localhost:1234 endpoint. Looked up per-call so a config edit takes
// effect on the next chat.send without restart.
//
// When the gateway is running inside a container, "localhost" /
// "127.0.0.1" inside the URL refers to the container — but the user
// almost certainly meant their host machine, where LM Studio is
// running. We rewrite the host segment to "host.docker.internal" so
// LM Studio Just Works without per-host configuration. The rewrite
// only fires when (1) we're in a container AND (2) the URL targets
// a loopback host. Real LAN/remote URLs pass through.
func (f *agentProviderFactory) lookupLMStudioBaseURL() string {
	const defaultURL = "http://localhost:1234/v1"
	raw := defaultURL
	if merged, err := config.MergedBytes(f.paths); err == nil {
		if v := gjson.GetBytes(merged, "models.providers.lmstudio.baseUrl"); v.Exists() && v.Str != "" {
			raw = v.Str
		}
	}
	return rewriteLoopbackForContainer(raw)
}

// inContainerOnce caches the /.dockerenv probe — file presence
// doesn't change for the life of the process.
var (
	inContainerOnce sync.Once
	inContainer     bool
)

// runningInContainer reports whether this process is in a Docker /
// OCI container by checking for the /.dockerenv flag file (Docker)
// and /run/.containerenv (Podman). Cached after first call.
func runningInContainer() bool {
	inContainerOnce.Do(func() {
		for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
			if _, err := os.Stat(p); err == nil {
				inContainer = true
				return
			}
		}
	})
	return inContainer
}

// rewriteLoopbackForContainer swaps localhost / 127.0.0.1 / ::1 in
// rawURL for "host.docker.internal" when the process is in a
// container. No-op outside containers, or when the URL targets a
// non-loopback host.
func rewriteLoopbackForContainer(rawURL string) string {
	return rewriteLoopback(rawURL, runningInContainer())
}

// rewriteLoopback is the pure function — takes the inContainer bool
// explicitly so tests can exercise both branches without filesystem
// stubbing.
func rewriteLoopback(rawURL string, inContainer bool) string {
	if !inContainer {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return rawURL
	}
	port := u.Port()
	if port != "" {
		u.Host = "host.docker.internal:" + port
	} else {
		u.Host = "host.docker.internal"
	}
	return u.String()
}
