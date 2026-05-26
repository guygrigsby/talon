package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/netutil"
	plugin "github.com/guygrigsby/talon/internal/plugin/host"
	"github.com/guygrigsby/talon/internal/plugin/native"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/subagents"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/guygrigsby/talon/internal/tools"
	"github.com/tidwall/gjson"
)

// configAgentResolver implements server.AgentResolver by reading talon's
// merged config on each call. Cheap enough that we don't cache: the merged
// view is parsed in-memory from already-cached overlay bytes.
//
// Model resolution precedence:
//
//  1. per-agent agents.list[id==X].model.primary (object form)
//  2. per-agent agents.list[id==X].model         (string shorthand)
//  3. agents.defaults.model.primary              (global default)
//
// First non-empty hit wins. If the agent isn't in agents.list at all,
// returns ErrAgentNotFound.
type configAgentResolver struct {
	paths talonpath.Paths
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
	if !agentExists(merged, agentID) {
		if _, ok, err := r.findSubagent(agentID); err != nil {
			return true, err
		} else if ok {
			return true, nil
		}
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
		if _, ok, err := r.findSubagent(agentID); err != nil {
			return "", err
		} else if !ok {
			return "", fmt.Errorf("%w: %q", server.ErrAgentNotFound, agentID)
		}
		if v := gjson.GetBytes(merged, "agents.defaults.workspace"); v.Exists() && v.Str != "" {
			return v.Str, nil
		}
		if main := gjson.GetBytes(merged, `agents.list.#(id=="main").workspace`); main.Exists() && main.Str != "" {
			return main.Str, nil
		}
		return "", fmt.Errorf("subagent %q has no inherited workspace and no agents.defaults.workspace", agentID)
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
// host is captured by reference; new plugins loaded after this factory
// is constructed light up automatically because ToolRouter walks
// host.List() per Specs/Run call.
func newToolRunnerFactory(host *plugin.Host, paths talonpath.Paths) func(workspace string, sub server.SubagentRunner) server.ToolRunner {
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
	// env are extra environment variables passed to the plugin
	// subprocess. KEY=VALUE form so they slot directly into
	// exec.Cmd.Env. Populated from entry.env (explicit user config) plus
	// auto-translated from native plugin config paths.
	env []string
}

func defaultPluginDefaults() pluginParseDefaults {
	return pluginParseDefaults{autoFillBuiltin: true}
}

type pluginParseDefaults struct {
	// autoFillBuiltin tells parsePluginSpecs to also include every
	// builtinPlugin name that isn't explicitly listed in
	// plugins.entries. The gateway sets this so a user who never
	// touches plugin config still gets the bundled tools; tests
	// leave it false so the focused-on-config-shape assertions
	// don't have to account for the auto-fill set.
	autoFillBuiltin bool
}

// parsePluginSpecs walks plugins.entries.<name> in the merged config
// and returns the native gRPC plugin subprocesses Talon should spawn.
func parsePluginSpecs(merged []byte, defaults pluginParseDefaults) []pluginSpec {
	var specs []pluginSpec
	seen := map[string]struct{}{}
	gjson.GetBytes(merged, "plugins.entries").ForEach(func(nameKey, entry gjson.Result) bool {
		seen[nameKey.Str] = struct{}{}
		if !entry.Get("enabled").Bool() {
			return true
		}
		env := buildPluginEnv(nameKey.Str, entry)
		if cmd := stringArray(entry.Get("cmd")); len(cmd) > 0 {
			specs = append(specs, pluginSpec{name: nameKey.Str, cmd: cmd, env: env})
			return true
		}
		// No explicit cmd — fall back to the first-party builtin registry.
		if cmd := server.BuiltinPluginCmd(nameKey.Str); len(cmd) > 0 {
			specs = append(specs, pluginSpec{name: nameKey.Str, cmd: cmd, env: env})
			return true
		}
		return true
	})
	// Auto-enable first-party plugins that aren't explicitly listed
	// in plugins.entries. Opt-in (gateway sets autoFillBuiltin=true;
	// tests default off) so the focused parse-shape tests don't
	// have to enumerate the entire bundled set. Plugins that need
	// config (telegram → botToken, brave → apiKey) still load
	// fine; the missing config surfaces as a channel-start / tool-
	// run error rather than a load failure.
	if defaults.autoFillBuiltin {
		for _, name := range server.BuiltinPluginNames() {
			if _, explicit := seen[name]; explicit {
				continue
			}
			cmd := server.BuiltinPluginCmd(name)
			if len(cmd) == 0 {
				continue
			}
			env := buildPluginEnv(name, gjson.Result{})
			specs = append(specs, pluginSpec{name: name, cmd: cmd, env: env})
		}
	}
	return specs
}

// buildPluginEnv assembles the env vars to hand to a plugin
// subprocess. Combines two sources, last-write-wins (so explicit
// user `env: {...}` overrides any auto-translation):
//
//  1. Auto-translation from native plugin config paths:
//     plugins.entries.brave.config.
//     webSearch.apiKey → BRAVE_API_KEY (or BRAVE_API_KEY_REF
//     when the value looks like an op:// / keychain:// reference).
//  2. Explicit entry.env: {KEY: VAL} block — user override.
//
// Other plugin names get just the explicit env block.
func buildPluginEnv(name string, entry gjson.Result) []string {
	out := []string{}
	switch name {
	case "anthropic":
		// Lets users put their API key (or an op:// / keychain://
		// reference) under plugins.entries.anthropic.config.apiKey
		// instead of an auth-profiles.json entry or a shell env var.
		if v := entry.Get("config.apiKey").Str; v != "" {
			if isSecretRef(v) {
				out = append(out, "ANTHROPIC_API_KEY_REF="+v)
			} else {
				out = append(out, "ANTHROPIC_API_KEY="+v)
			}
		}
	case "brave":
		if v := entry.Get("config.webSearch.apiKey").Str; v != "" {
			if isSecretRef(v) {
				out = append(out, "BRAVE_API_KEY_REF="+v)
			} else {
				out = append(out, "BRAVE_API_KEY="+v)
			}
		}
	case "whisper":
		// Whisper uses an OpenAI key; auto-translate from the
		// well-known openai-whisper-api skill config path.
		if v := entry.Get("config.apiKey").Str; v != "" {
			if isSecretRef(v) {
				out = append(out, "OPENAI_API_KEY_REF="+v)
			} else {
				out = append(out, "OPENAI_API_KEY="+v)
			}
		}
	}
	// Explicit env block. Iterate in alphabetic key order so the
	// resulting slice is deterministic for tests.
	if env := entry.Get("env"); env.IsObject() {
		keys := []string{}
		env.ForEach(func(k, _ gjson.Result) bool {
			keys = append(keys, k.Str)
			return true
		})
		sortStrings(keys)
		for _, k := range keys {
			v := env.Get(escapeForGjson(k)).String()
			out = append(out, k+"="+v)
		}
	}
	return out
}

// isSecretRef reports whether v looks like an op:// or keychain://
// reference. Mirrors internal/secrets.IsReference but inlined
// here to avoid the import cycle (cmd/talon already imports
// internal/secrets transitively but not directly here).
func isSecretRef(v string) bool {
	return strings.HasPrefix(v, "op://") || strings.HasPrefix(v, "keychain://")
}

// escapeForGjson wraps a key in double-brackets when it contains
// characters gjson treats as path separators. Empty / safe keys
// pass through unchanged.
func escapeForGjson(k string) string {
	if !strings.ContainsAny(k, ".[") {
		return k
	}
	return "[\"" + strings.ReplaceAll(k, `"`, `\"`) + "\"]"
}

// sortStrings is the stdlib sort.Strings under a name that
// doesn't collide with the existing helper in tools.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
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
// plugin entries, and dispatches each one through native.Spawn. Failures log
// and skip; a broken plugin shouldn't take down chat.
func loadConfiguredPlugins(
	ctx context.Context,
	host *plugin.Host,
	paths talonpath.Paths,
	nativeFactory native.HostServerFactory,
) {
	merged, err := config.MergedBytes(paths)
	if err != nil {
		slog.Error("plugins read merged config failed", "err", err)
		return
	}
	for _, spec := range parsePluginSpecs(merged, defaultPluginDefaults()) {
		inst, err := native.Spawn(ctx, spec.name, nativeFactory,
			native.LoadOptions{Cmd: spec.cmd, Env: spec.env},
			host.Unregister)
		if err == nil {
			if regErr := host.RegisterInstance(inst); regErr != nil {
				inst.Stop()
				err = regErr
			}
		}
		if err != nil {
			slog.Error("plugin load failed", "plugin", spec.name, "kind", "native", "err", err)
			continue
		}
		slog.Info("plugin loaded",
			"plugin", spec.name,
			"kind", "native",
			"tools", len(inst.Manifest.GetOffersTools()),
			"providers", len(inst.Manifest.GetOffersProviders()),
			"channels", len(inst.Manifest.GetOffersChannels()),
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
func startConfiguredChannels(ctx context.Context, host *plugin.Host, paths talonpath.Paths, runner plugin.SessionRunner) []*plugin.ChannelDispatcher {
	merged, err := config.MergedBytes(paths)
	if err != nil {
		slog.Error("channels read merged config failed", "err", err)
		return nil
	}
	bindings := parseChannelBindings(merged, collectChannelOffers(host))
	out := make([]*plugin.ChannelDispatcher, 0, len(bindings))
	for _, b := range bindings {
		resolvedConfig, err := resolveChannelConfigSecrets(b.Binding.ConfigJSON, resolveSecretRef)
		if err != nil {
			slog.Error("channel config secret resolution failed",
				"plugin", b.PluginName, "channel", b.Binding.ChannelName, "err", err)
			continue
		}
		b.Binding.ConfigJSON = resolvedConfig

		inst := host.Get(b.PluginName)
		if inst == nil {
			continue
		}
		d, err := plugin.NewChannelDispatcher(inst, b.Binding, runner)
		if err != nil {
			slog.Error("channel dispatcher failed",
				"plugin", b.PluginName, "channel", b.Binding.ChannelName, "err", err)
			continue
		}
		d.Start(ctx)
		out = append(out, d)
		slog.Info("channel dispatching",
			"plugin", b.PluginName, "channel", b.Binding.ChannelName, "agent", b.Binding.AgentID)
	}
	return out
}

func resolveChannelConfigSecrets(raw []byte, resolve func(string) (string, error)) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	if resolve == nil {
		resolve = resolveSecretRef
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("parse channel config: %w", err)
	}
	if err := resolveSensitiveLeaves(v, "", resolve); err != nil {
		return nil, err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal channel config: %w", err)
	}
	return out, nil
}

func resolveSensitiveLeaves(v any, key string, resolve func(string) (string, error)) error {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if s, ok := child.(string); ok && s != "" && secrets.IsSensitiveKey(k) {
				resolved, err := resolve(s)
				if err != nil {
					return fmt.Errorf("resolve %s: %w", k, err)
				}
				x[k] = resolved
				continue
			}
			if err := resolveSensitiveLeaves(child, k, resolve); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := resolveSensitiveLeaves(child, key, resolve); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *configAgentResolver) PrimaryModel(agentID string) (provider.ModelID, error) {
	merged, err := config.MergedBytes(r.paths)
	if err != nil {
		return "", fmt.Errorf("read merged config: %w", err)
	}
	agent := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID))
	if !agent.Exists() {
		if def, ok, err := r.findSubagent(agentID); err != nil {
			return "", err
		} else if ok {
			if def.Model != "" {
				return provider.ModelID(def.Model), nil
			}
			if v := gjson.GetBytes(merged, "agents.defaults.model.primary"); v.Exists() && v.Str != "" {
				return provider.ModelID(v.Str), nil
			}
			return "", fmt.Errorf("subagent %q has no model and no agents.defaults.model.primary", agentID)
		}
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

func (r *configAgentResolver) findSubagent(agentID string) (subagents.Definition, bool, error) {
	def, ok, err := subagents.Find(r.paths.Talon.SubagentsDir(), agentID)
	if err != nil {
		return subagents.Definition{}, false, fmt.Errorf("read subagents: %w", err)
	}
	return def, ok, nil
}

func agentExists(merged []byte, agentID string) bool {
	return gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID)).Exists()
}

// agentProviderFactory implements server.ProviderFactory. Resolution
// order: native built-ins (openai, deepseek) first, then any loaded
// plugin whose manifest offers the requested provider name. Only fails
// with ErrProviderUnavailable when neither side recognizes the name —
// that's the signal the model targeted a provider nothing can serve.
type agentProviderFactory struct {
	paths talonpath.Paths
	host  *plugin.Host // optional; nil disables plugin-served providers
}

// authProfilePath returns the auth-profiles.json path for agentID under
// Talon's state root.
func (f *agentProviderFactory) authProfilePath(agentID string) string {
	return filepath.Join(f.paths.Talon.AgentDir(agentID), "agent", "auth-profiles.json")
}

func (f *agentProviderFactory) For(providerName, agentID string) (provider.Provider, error) {
	// All providers come from plugins. The openai-compat plugin
	// serves openai/deepseek/mistral/mlx/ollama/etc as multi-tenant
	// entries; anthropic is a dedicated plugin. Any provider not
	// offered by a loaded plugin is unavailable — there's no in-
	// tree fallback.
	//
	// One exception: lmstudio retains a host-side path because of
	// the container-aware base-URL rewrite (host.docker.internal)
	// and the per-agent auth-profiles.json fallback. Both can move
	// into the openai-compat plugin in a follow-up; the rewrite
	// involves paths.IsContainer() which is a host-side concern.
	if f.host != nil {
		if inst := f.host.ProviderByName(providerName); inst != nil {
			return plugin.NewPluginProvider(providerName, inst.Client), nil
		}
	}
	authPath := f.authProfilePath(agentID)
	switch providerName {
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
		if key, err = resolveSecretRef(key); err != nil {
			return nil, fmt.Errorf("lmstudio: resolve key: %w", err)
		}
		baseURL := f.lookupLMStudioBaseURL()
		return openai.New(openai.Options{
			APIKey:      key,
			BaseURL:     baseURL,
			Name:        "lmstudio",
			ProviderKey: "lmstudio",
		}), nil
	}
	return nil, fmt.Errorf("%w: %q (no loaded plugin offers it; enable openai-compat with this provider in config, or load a plugin that advertises it)",
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
