package talonconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/viper"
	"github.com/tidwall/gjson"
)

// NativeConfig is the proposed Talon-owned config shape. It is deliberately
// not a one-for-one copy of the old OpenClaw JSON envelope.
type NativeConfig struct {
	Gateway   GatewayConfig    `mapstructure:"gateway"`
	Agent     ChatAgentConfig  `mapstructure:"agent"`
	Subagents []SubagentConfig `mapstructure:"subagents"`
	Memory    MemoryConfig     `mapstructure:"memory"`
	Tools     ToolsConfig      `mapstructure:"tools"`
	Models    ModelsConfig     `mapstructure:"models"`
	Telegram  TelegramConfig   `mapstructure:"-"`
	Plugins   PluginsConfig    `mapstructure:"plugins"`
}

type GatewayConfig struct {
	Mode                   string `mapstructure:"mode"`
	Bind                   string `mapstructure:"bind"`
	Port                   int64  `mapstructure:"port"`
	AuthMode               string `mapstructure:"auth_mode"`
	AuthToken              string `mapstructure:"auth_token_ref"`
	TailscaleMode          string `mapstructure:"tailscale_mode"`
	ControlUIRoot          string `mapstructure:"control_ui_root"`
	ControlUIAllowInsecure *bool  `mapstructure:"control_ui_allow_insecure_auth"`
}

type ChatAgentConfig struct {
	Model        string       `mapstructure:"model"`
	Fallbacks    []string     `mapstructure:"fallback_models"`
	Workspace    string       `mapstructure:"workspace"`
	ToolsProfile string       `mapstructure:"tools_profile"`
	ModelAliases []ModelAlias `mapstructure:"model_aliases"`
	MaxTurns     int64        `mapstructure:"max_turns"`
	SystemPrompt string       `mapstructure:"system_prompt"`
}

type ModelAlias struct {
	Model string `mapstructure:"model"`
	Alias string `mapstructure:"alias"`
}

type SubagentConfig struct {
	ID           string `mapstructure:"id"`
	Name         string `mapstructure:"name"`
	Model        string `mapstructure:"model"`
	ToolsProfile string `mapstructure:"tools_profile"`
}

type MemoryConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Path    string `mapstructure:"path"`
	Model   string `mapstructure:"model"`
}

type ToolsConfig struct {
	Profile           string `mapstructure:"profile"`
	WebSearchEnabled  *bool  `mapstructure:"web_search_enabled"`
	WebSearchProvider string `mapstructure:"web_search_provider"`
	WebSearchAPIKey   string `mapstructure:"web_search_api_key_ref"`
}

type ModelsConfig struct {
	Providers map[string]ModelProviderConfig `mapstructure:"providers"`
}

type ModelProviderConfig struct {
	ID      string        `mapstructure:"-"`
	API     string        `mapstructure:"api"`
	BaseURL string        `mapstructure:"base_url"`
	APIKey  string        `mapstructure:"api_key_ref"`
	Models  []ModelConfig `mapstructure:"models"`
}

type ModelConfig struct {
	ID            string    `mapstructure:"id"`
	Name          string    `mapstructure:"name"`
	API           string    `mapstructure:"api"`
	ContextWindow int64     `mapstructure:"context_window"`
	MaxTokens     int64     `mapstructure:"max_tokens"`
	Input         []string  `mapstructure:"input"`
	Reasoning     *bool     `mapstructure:"reasoning"`
	Cost          ModelCost `mapstructure:",squash"`
}

type ModelCost struct {
	Input      *float64 `mapstructure:"cost_input"`
	Output     *float64 `mapstructure:"cost_output"`
	CacheRead  *float64 `mapstructure:"cost_cache_read"`
	CacheWrite *float64 `mapstructure:"cost_cache_write"`
}

type TelegramConfig struct {
	Enabled        bool     `mapstructure:"enabled"`
	BotToken       string   `mapstructure:"bot_token_ref"`
	AllowFrom      []string `mapstructure:"allow_from"`
	AgentID        string   `mapstructure:"agent_id"`
	RequireMention *bool    `mapstructure:"require_mention"`
}

type PluginsConfig struct {
	Enabled            []string `mapstructure:"enabled"`
	Deny               []string `mapstructure:"deny"`
	LoadPaths          []string `mapstructure:"load_paths"`
	LegacyOpenClawShim bool     `mapstructure:"legacy_openclaw_shim"`
}

type MigrationReport struct {
	SourceTopLevelKeys []string
	StateCandidates    []string
	DropCandidates     []string
	SecretCounts       map[string]int
	LegacyPluginShim   bool
}

// FromLegacyJSON converts the current JSON config envelope into the proposed
// native shape. It keeps enough information to preserve working chat, tools,
// memory, models, and Telegram while intentionally not preserving OpenClaw's
// per-agent workspace/persona paradigm for subagents.
func FromLegacyJSON(raw []byte) (NativeConfig, MigrationReport, error) {
	if !gjson.ValidBytes(raw) {
		return NativeConfig{}, MigrationReport{}, fmt.Errorf("legacy config is not valid JSON")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return NativeConfig{}, MigrationReport{}, err
	}

	var cfg NativeConfig
	cfg.Gateway = gatewayFromLegacy(raw)
	cfg.Agent = chatAgentFromLegacy(raw)
	cfg.Subagents = subagentsFromLegacy(raw)
	cfg.Memory = memoryFromLegacy(raw)
	cfg.Tools = toolsFromLegacy(raw)
	cfg.Models = modelsFromLegacy(raw)
	cfg.Telegram = telegramFromLegacy(raw)
	cfg.Plugins = pluginsFromLegacy(raw)

	report := MigrationReport{
		SourceTopLevelKeys: sortedKeys(decoded),
		StateCandidates: []string{
			"meta",
			"session",
			"wizard",
			"chat.handler",
			"hooks.internal",
			"agents.defaults.embeddedHarness",
		},
		DropCandidates: []string{
			"agents.list[].workspace for subagents only",
			"agents.list[].agentDir",
			"workspace*/.openclaw/workspace-state.json",
			"~/.talon/openclaw.json.* backups after TOML cutover",
		},
		SecretCounts:     secretCounts(raw),
		LegacyPluginShim: cfg.Plugins.LegacyOpenClawShim,
	}
	return cfg, report, nil
}

func gatewayFromLegacy(raw []byte) GatewayConfig {
	g := GatewayConfig{
		Mode:          stringOr(raw, "gateway.mode", "local"),
		Bind:          stringOr(raw, "gateway.bind", "loopback"),
		Port:          intOr(raw, "gateway.port", 18789),
		AuthMode:      gjson.GetBytes(raw, "gateway.auth.mode").Str,
		AuthToken:     gjson.GetBytes(raw, "gateway.auth.token").Str,
		TailscaleMode: gjson.GetBytes(raw, "gateway.tailscale.mode").Str,
		ControlUIRoot: gjson.GetBytes(raw, "gateway.controlUi.root").Str,
	}
	if v := gjson.GetBytes(raw, "gateway.controlUi.allowInsecureAuth"); v.Exists() {
		b := v.Bool()
		g.ControlUIAllowInsecure = &b
	}
	return g
}

func chatAgentFromLegacy(raw []byte) ChatAgentConfig {
	a := ChatAgentConfig{
		Model:        gjson.GetBytes(raw, "agents.defaults.model.primary").Str,
		Workspace:    gjson.GetBytes(raw, "agents.defaults.workspace").Str,
		ToolsProfile: gjson.GetBytes(raw, "tools.profile").Str,
		MaxTurns:     gjson.GetBytes(raw, "agents.defaults.maxTurns").Int(),
		SystemPrompt: gjson.GetBytes(raw, "agents.defaults.systemPrompt").Str,
	}
	a.Fallbacks = stringArray(gjson.GetBytes(raw, "agents.defaults.model.fallbacks"))
	gjson.GetBytes(raw, "agents.defaults.models").ForEach(func(k, v gjson.Result) bool {
		if alias := v.Get("alias"); alias.Exists() && alias.Type == gjson.String && alias.Str != "" {
			a.ModelAliases = append(a.ModelAliases, ModelAlias{Model: k.Str, Alias: alias.Str})
		}
		return true
	})
	sort.Slice(a.ModelAliases, func(i, j int) bool { return a.ModelAliases[i].Model < a.ModelAliases[j].Model })

	if main := findAgent(raw, "main"); main.Exists() {
		if model := modelString(main.Get("model")); model != "" {
			a.Model = model
		}
		if workspace := main.Get("workspace").Str; workspace != "" {
			a.Workspace = workspace
		}
		if profile := main.Get("tools.profile").Str; profile != "" {
			a.ToolsProfile = profile
		}
		if maxTurns := main.Get("maxTurns").Int(); maxTurns > 0 {
			a.MaxTurns = maxTurns
		}
		if prompt := main.Get("systemPrompt").Str; prompt != "" {
			a.SystemPrompt = prompt
		}
	}
	return a
}

func subagentsFromLegacy(raw []byte) []SubagentConfig {
	var out []SubagentConfig
	gjson.GetBytes(raw, "agents.list").ForEach(func(_, a gjson.Result) bool {
		id := a.Get("id").Str
		if id == "" || id == "main" {
			return true
		}
		out = append(out, SubagentConfig{
			ID:           id,
			Name:         a.Get("name").Str,
			Model:        modelString(a.Get("model")),
			ToolsProfile: a.Get("tools.profile").Str,
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func memoryFromLegacy(raw []byte) MemoryConfig {
	return MemoryConfig{
		Enabled: gjson.GetBytes(raw, "memory.enabled").Bool(),
		Path:    gjson.GetBytes(raw, "memory.path").Str,
		Model:   gjson.GetBytes(raw, "memory.model").Str,
	}
}

func toolsFromLegacy(raw []byte) ToolsConfig {
	t := ToolsConfig{
		Profile:           gjson.GetBytes(raw, "tools.profile").Str,
		WebSearchProvider: gjson.GetBytes(raw, "tools.web.search.provider").Str,
		WebSearchAPIKey:   gjson.GetBytes(raw, "plugins.entries.brave.config.webSearch.apiKey").Str,
	}
	if v := gjson.GetBytes(raw, "tools.web.search.enabled"); v.Exists() {
		b := v.Bool()
		t.WebSearchEnabled = &b
	}
	return t
}

func modelsFromLegacy(raw []byte) ModelsConfig {
	byID := map[string]*ModelProviderConfig{}
	ensure := func(id string) *ModelProviderConfig {
		if id == "" {
			return nil
		}
		if p := byID[id]; p != nil {
			return p
		}
		p := &ModelProviderConfig{ID: id}
		byID[id] = p
		return p
	}

	gjson.GetBytes(raw, "models.providers").ForEach(func(name, prov gjson.Result) bool {
		p := ensure(name.Str)
		if p == nil {
			return true
		}
		p.API = prov.Get("api").Str
		p.BaseURL = prov.Get("baseUrl").Str
		prov.Get("models").ForEach(func(_, m gjson.Result) bool {
			p.Models = append(p.Models, modelFromLegacy(m))
			return true
		})
		return true
	})

	gjson.GetBytes(raw, "plugins.entries.openai-compat.config.providers").ForEach(func(name, prov gjson.Result) bool {
		p := ensure(name.Str)
		if p == nil {
			return true
		}
		if v := prov.Get("baseUrl").Str; v != "" {
			p.BaseURL = v
		}
		if v := prov.Get("apiKey").Str; v != "" {
			p.APIKey = v
		}
		return true
	})
	if key := gjson.GetBytes(raw, "plugins.entries.anthropic.config.apiKey").Str; key != "" {
		if p := ensure("anthropic"); p != nil {
			p.APIKey = key
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := ModelsConfig{Providers: make(map[string]ModelProviderConfig, len(ids))}
	for _, id := range ids {
		p := *byID[id]
		sort.Slice(p.Models, func(i, j int) bool { return p.Models[i].ID < p.Models[j].ID })
		out.Providers[id] = p
	}
	return out
}

func modelFromLegacy(m gjson.Result) ModelConfig {
	out := ModelConfig{
		ID:            m.Get("id").Str,
		Name:          m.Get("name").Str,
		API:           m.Get("api").Str,
		ContextWindow: m.Get("contextWindow").Int(),
		MaxTokens:     m.Get("maxTokens").Int(),
		Input:         stringArray(m.Get("input")),
	}
	if v := m.Get("reasoning"); v.Exists() {
		b := v.Bool()
		out.Reasoning = &b
	}
	if cost := m.Get("cost"); cost.Exists() {
		out.Cost = costFromLegacy(cost)
	}
	return out
}

func costFromLegacy(cost gjson.Result) ModelCost {
	var out ModelCost
	if v := cost.Get("input"); v.Exists() {
		f := v.Float()
		out.Input = &f
	}
	if v := cost.Get("output"); v.Exists() {
		f := v.Float()
		out.Output = &f
	}
	if v := cost.Get("cacheRead"); v.Exists() {
		f := v.Float()
		out.CacheRead = &f
	}
	if v := cost.Get("cacheWrite"); v.Exists() {
		f := v.Float()
		out.CacheWrite = &f
	}
	return out
}

func telegramFromLegacy(raw []byte) TelegramConfig {
	t := TelegramConfig{
		Enabled:   gjson.GetBytes(raw, "channels.telegram.enabled").Bool(),
		BotToken:  gjson.GetBytes(raw, "channels.telegram.botToken").Str,
		AllowFrom: stringArray(gjson.GetBytes(raw, "channels.telegram.allowFrom")),
		AgentID:   gjson.GetBytes(raw, "channels.telegram.agentId").Str,
	}
	if v := gjson.GetBytes(raw, "channels.telegram.groups.*.requireMention"); v.Exists() {
		b := v.Bool()
		t.RequireMention = &b
	}
	return t
}

func pluginsFromLegacy(raw []byte) PluginsConfig {
	var p PluginsConfig
	gjson.GetBytes(raw, "plugins.entries").ForEach(func(name, entry gjson.Result) bool {
		if entry.Get("enabled").Bool() {
			p.Enabled = append(p.Enabled, name.Str)
		}
		return true
	})
	p.Deny = stringArray(gjson.GetBytes(raw, "plugins.deny"))
	p.LoadPaths = stringArray(gjson.GetBytes(raw, "plugins.load.paths"))
	p.LegacyOpenClawShim = len(p.LoadPaths) > 0 || gjson.GetBytes(raw, "plugins.entries.openclaw-shim").Exists()
	sort.Strings(p.Enabled)
	sort.Strings(p.Deny)
	sort.Strings(p.LoadPaths)
	return p
}

func findAgent(raw []byte, id string) gjson.Result {
	var out gjson.Result
	gjson.GetBytes(raw, "agents.list").ForEach(func(_, a gjson.Result) bool {
		if a.Get("id").Str == id {
			out = a
			return false
		}
		return true
	})
	return out
}

func modelString(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.Str
	}
	if primary := v.Get("primary").Str; primary != "" {
		return primary
	}
	return ""
}

func stringArray(v gjson.Result) []string {
	var out []string
	v.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String && item.Str != "" {
			out = append(out, item.Str)
		}
		return true
	})
	return out
}

func stringOr(raw []byte, path, fallback string) string {
	if v := gjson.GetBytes(raw, path); v.Exists() && v.Str != "" {
		return v.Str
	}
	return fallback
}

func intOr(raw []byte, path string, fallback int64) int64 {
	if v := gjson.GetBytes(raw, path); v.Exists() && v.Int() != 0 {
		return v.Int()
	}
	return fallback
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func secretCounts(raw []byte) map[string]int {
	counts := map[string]int{}
	var walk func(prefix string, v gjson.Result)
	walk = func(prefix string, v gjson.Result) {
		if v.IsObject() {
			v.ForEach(func(k, child gjson.Result) bool {
				next := k.Str
				if prefix != "" {
					next = prefix + "." + next
				}
				walk(next, child)
				return true
			})
			return
		}
		if v.IsArray() {
			v.ForEach(func(_, child gjson.Result) bool {
				walk(prefix+".[]", child)
				return true
			})
			return
		}
		if !looksSecretPath(prefix) {
			return
		}
		counts[secretKind(v.String())]++
	}
	walk("", gjson.ParseBytes(raw))
	return counts
}

func looksSecretPath(path string) bool {
	lower := strings.ToLower(path)
	for _, needle := range []string{"apikey", "bottoken", "auth.token", "privatekey", "refresh", "access", "password", "secret"} {
		if strings.Contains(lower, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func secretKind(value string) string {
	switch {
	case value == "":
		return "empty"
	case strings.HasPrefix(value, "op://"):
		return "op-ref"
	case strings.HasPrefix(value, "keychain://"):
		return "keychain-ref"
	case strings.HasPrefix(value, "env:"):
		return "env-ref"
	default:
		return "literal"
	}
}

type MarshalOptions struct {
	RedactSecrets bool
}

const redactedLiteral = "<redacted:literal>"

// ToLegacyJSON adapts native TOML config back into the legacy JSON envelope
// used by the rest of Talon today. fallbackLegacy is treated as compatibility
// ballast: unmigrated sections are preserved, and redacted native literals are
// resolved from the old JSON so an appended migration preview remains usable.
func ToLegacyJSON(cfg NativeConfig, fallbackLegacy []byte) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(fallbackLegacy)) > 0 {
		if err := json.Unmarshal(fallbackLegacy, &root); err != nil {
			return nil, fmt.Errorf("parse fallback legacy config: %w", err)
		}
	}

	applyGatewayLegacy(root, cfg, fallbackLegacy)
	applyAgentsLegacy(root, cfg)
	applyMemoryLegacy(root, cfg)
	applyToolsLegacy(root, cfg, fallbackLegacy)
	applyModelsLegacy(root, cfg, fallbackLegacy)
	applyTelegramLegacy(root, cfg, fallbackLegacy)
	applyPluginsLegacy(root, cfg)

	return json.Marshal(root)
}

func applyGatewayLegacy(root map[string]any, cfg NativeConfig, fallback []byte) {
	setString(root, cfg.Gateway.Mode, "gateway", "mode")
	setString(root, cfg.Gateway.Bind, "gateway", "bind")
	setInt(root, cfg.Gateway.Port, "gateway", "port")
	setString(root, cfg.Gateway.AuthMode, "gateway", "auth", "mode")
	if token := usableSecret(cfg.Gateway.AuthToken, fallbackString(fallback, "gateway.auth.token")); token != "" {
		setPath(root, token, "gateway", "auth", "token")
	}
	setString(root, cfg.Gateway.TailscaleMode, "gateway", "tailscale", "mode")
	setString(root, cfg.Gateway.ControlUIRoot, "gateway", "controlUi", "root")
	if cfg.Gateway.ControlUIAllowInsecure != nil {
		setPath(root, *cfg.Gateway.ControlUIAllowInsecure, "gateway", "controlUi", "allowInsecureAuth")
	}
}

func applyAgentsLegacy(root map[string]any, cfg NativeConfig) {
	setString(root, cfg.Agent.Model, "agents", "defaults", "model", "primary")
	setStrings(root, cfg.Agent.Fallbacks, "agents", "defaults", "model", "fallbacks")
	setString(root, cfg.Agent.Workspace, "agents", "defaults", "workspace")
	setString(root, cfg.Agent.ToolsProfile, "agents", "defaults", "tools", "profile")
	setInt(root, cfg.Agent.MaxTurns, "agents", "defaults", "maxTurns")
	setString(root, cfg.Agent.SystemPrompt, "agents", "defaults", "systemPrompt")

	if len(cfg.Agent.ModelAliases) > 0 {
		aliases := map[string]any{}
		for _, alias := range cfg.Agent.ModelAliases {
			if alias.Model == "" || alias.Alias == "" {
				continue
			}
			aliases[alias.Model] = map[string]any{"alias": alias.Alias}
		}
		if len(aliases) > 0 {
			setPath(root, aliases, "agents", "defaults", "models")
		}
	}

	agents := make([]any, 0, 1+len(cfg.Subagents))
	main := map[string]any{"id": "main"}
	if cfg.Agent.Model != "" {
		main["model"] = cfg.Agent.Model
	}
	if cfg.Agent.Workspace != "" {
		main["workspace"] = cfg.Agent.Workspace
	}
	if cfg.Agent.ToolsProfile != "" {
		main["tools"] = map[string]any{"profile": cfg.Agent.ToolsProfile}
	}
	if cfg.Agent.MaxTurns > 0 {
		main["maxTurns"] = cfg.Agent.MaxTurns
	}
	if cfg.Agent.SystemPrompt != "" {
		main["systemPrompt"] = cfg.Agent.SystemPrompt
	}
	agents = append(agents, main)
	for _, sub := range cfg.Subagents {
		if sub.ID == "" {
			continue
		}
		entry := map[string]any{"id": sub.ID}
		if sub.Name != "" {
			entry["name"] = sub.Name
		}
		if sub.Model != "" {
			entry["model"] = sub.Model
		}
		if sub.ToolsProfile != "" {
			entry["tools"] = map[string]any{"profile": sub.ToolsProfile}
		}
		agents = append(agents, entry)
	}
	setPath(root, agents, "agents", "list")
}

func applyMemoryLegacy(root map[string]any, cfg NativeConfig) {
	setPath(root, cfg.Memory.Enabled, "memory", "enabled")
	setString(root, cfg.Memory.Path, "memory", "path")
	setString(root, cfg.Memory.Model, "memory", "model")
}

func applyToolsLegacy(root map[string]any, cfg NativeConfig, fallback []byte) {
	setString(root, cfg.Tools.Profile, "tools", "profile")
	if cfg.Tools.WebSearchEnabled != nil {
		setPath(root, *cfg.Tools.WebSearchEnabled, "tools", "web", "search", "enabled")
	}
	setString(root, cfg.Tools.WebSearchProvider, "tools", "web", "search", "provider")
	if key := usableSecret(cfg.Tools.WebSearchAPIKey, fallbackString(fallback, "plugins.entries.brave.config.webSearch.apiKey")); key != "" {
		setPath(root, key, "plugins", "entries", "brave", "config", "webSearch", "apiKey")
	}
}

func applyModelsLegacy(root map[string]any, cfg NativeConfig, fallback []byte) {
	providers := map[string]any{}
	providerIDs := make([]string, 0, len(cfg.Models.Providers))
	for id := range cfg.Models.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, id := range providerIDs {
		provider := cfg.Models.Providers[id]
		entry := map[string]any{}
		if provider.API != "" {
			entry["api"] = provider.API
		}
		if provider.BaseURL != "" {
			entry["baseUrl"] = provider.BaseURL
		}
		if len(provider.Models) > 0 {
			models := make([]any, 0, len(provider.Models))
			for _, model := range provider.Models {
				models = append(models, modelToLegacy(model))
			}
			entry["models"] = models
		}
		providers[id] = entry

		apiKey := usableSecret(provider.APIKey, providerFallbackAPIKey(fallback, id))
		if id == "anthropic" {
			if apiKey != "" {
				setPath(root, apiKey, "plugins", "entries", "anthropic", "config", "apiKey")
			}
			continue
		}
		if provider.BaseURL != "" {
			setPath(root, provider.BaseURL, "plugins", "entries", "openai-compat", "config", "providers", id, "baseUrl")
		}
		if apiKey != "" {
			setPath(root, apiKey, "plugins", "entries", "openai-compat", "config", "providers", id, "apiKey")
		}
	}
	setPath(root, providers, "models", "providers")
}

func applyTelegramLegacy(root map[string]any, cfg NativeConfig, fallback []byte) {
	setPath(root, cfg.Telegram.Enabled, "channels", "telegram", "enabled")
	if token := usableSecret(cfg.Telegram.BotToken, fallbackString(fallback, "channels.telegram.botToken")); token != "" {
		setPath(root, token, "channels", "telegram", "botToken")
	}
	setStrings(root, cfg.Telegram.AllowFrom, "channels", "telegram", "allowFrom")
	setString(root, cfg.Telegram.AgentID, "channels", "telegram", "agentId")
	if cfg.Telegram.RequireMention != nil {
		setPath(root, *cfg.Telegram.RequireMention, "channels", "telegram", "groups", "*", "requireMention")
	}
}

func applyPluginsLegacy(root map[string]any, cfg NativeConfig) {
	for _, name := range cfg.Plugins.Enabled {
		if name == "" {
			continue
		}
		setPath(root, true, "plugins", "entries", name, "enabled")
	}
	setStrings(root, cfg.Plugins.Deny, "plugins", "deny")
	setStrings(root, cfg.Plugins.LoadPaths, "plugins", "load", "paths")
	if len(cfg.Plugins.LoadPaths) > 0 {
		setStrings(root, cfg.Plugins.LoadPaths, "plugins", "bundled", "paths")
		setString(root, cfg.Plugins.LoadPaths[0], "plugins", "bundled", "path")
	}
}

func modelToLegacy(model ModelConfig) map[string]any {
	out := map[string]any{}
	if model.ID != "" {
		out["id"] = model.ID
	}
	if model.Name != "" {
		out["name"] = model.Name
	}
	if model.API != "" {
		out["api"] = model.API
	}
	if model.ContextWindow > 0 {
		out["contextWindow"] = model.ContextWindow
	}
	if model.MaxTokens > 0 {
		out["maxTokens"] = model.MaxTokens
	}
	if len(model.Input) > 0 {
		out["input"] = append([]string(nil), model.Input...)
	}
	if model.Reasoning != nil {
		out["reasoning"] = *model.Reasoning
	}
	if model.Cost.hasAny() {
		cost := map[string]any{}
		if model.Cost.Input != nil {
			cost["input"] = *model.Cost.Input
		}
		if model.Cost.Output != nil {
			cost["output"] = *model.Cost.Output
		}
		if model.Cost.CacheRead != nil {
			cost["cacheRead"] = *model.Cost.CacheRead
		}
		if model.Cost.CacheWrite != nil {
			cost["cacheWrite"] = *model.Cost.CacheWrite
		}
		out["cost"] = cost
	}
	return out
}

func providerFallbackAPIKey(fallback []byte, providerID string) string {
	if providerID == "anthropic" {
		return fallbackString(fallback, "plugins.entries.anthropic.config.apiKey")
	}
	return gjson.GetBytes(fallback, "plugins.entries.openai-compat.config.providers."+escapeGJSONKey(providerID)+".apiKey").Str
}

func usableSecret(value, fallback string) string {
	if value == "" || value == redactedLiteral {
		return fallback
	}
	return value
}

func fallbackString(raw []byte, path string) string {
	if len(raw) == 0 {
		return ""
	}
	return gjson.GetBytes(raw, path).Str
}

func setString(root map[string]any, value string, path ...string) {
	if value == "" {
		return
	}
	setPath(root, value, path...)
}

func setStrings(root map[string]any, values []string, path ...string) {
	if len(values) == 0 {
		return
	}
	setPath(root, append([]string(nil), values...), path...)
}

func setInt(root map[string]any, value int64, path ...string) {
	if value == 0 {
		return
	}
	setPath(root, value, path...)
}

func setPath(root map[string]any, value any, path ...string) {
	if len(path) == 0 {
		return
	}
	m := root
	for _, key := range path[:len(path)-1] {
		m = ensureMap(m, key)
	}
	m[path[len(path)-1]] = value
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	next := map[string]any{}
	parent[key] = next
	return next
}

func escapeGJSONKey(key string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `.`, `\.`, `*`, `\*`, `?`, `\?`, `#`, `\#`, `|`, `\|`)
	return replacer.Replace(key)
}

// NewViper returns the configured Viper instance Talon will use for native
// config reads. The migration command uses the same path to validate generated
// TOML before printing it.
func NewViper() *viper.Viper {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.SetEnvPrefix("TALON")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	return v
}

func LoadFile(path string) (NativeConfig, error) {
	v := NewViper()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return NativeConfig{}, err
	}
	return decodeViper(v)
}

func ReadTOMLBytes(raw []byte) (NativeConfig, error) {
	v := NewViper()
	if err := v.ReadConfig(bytes.NewReader(raw)); err != nil {
		return NativeConfig{}, err
	}
	return decodeViper(v)
}

func ValidateTOMLBytes(raw []byte) error {
	_, err := ReadTOMLBytes(raw)
	return err
}

func decodeViper(v *viper.Viper) (NativeConfig, error) {
	var cfg NativeConfig
	if err := v.Unmarshal(&cfg); err != nil {
		return NativeConfig{}, err
	}
	var channels struct {
		Telegram TelegramConfig `mapstructure:"telegram"`
	}
	if err := v.UnmarshalKey("channels", &channels); err != nil {
		return NativeConfig{}, err
	}
	cfg.Telegram = channels.Telegram
	if cfg.Models.Providers == nil {
		cfg.Models.Providers = map[string]ModelProviderConfig{}
	}
	for id, provider := range cfg.Models.Providers {
		provider.ID = id
		cfg.Models.Providers[id] = provider
	}
	return cfg, nil
}

func MarshalTOML(cfg NativeConfig, opts MarshalOptions) []byte {
	var b strings.Builder
	w := tomlWriter{b: &b, opts: opts}
	w.comment("Generated preview from legacy talon JSON. Review before writing ~/.talon/config.toml.")
	w.comment("Legacy JSON may still supply redacted or not-yet-migrated values during cutover.")
	w.comment("The main chat agent keeps its workspace Markdown files; subagents migrate as task/model profiles.")
	w.blank()
	w.section("gateway")
	w.kvString("mode", cfg.Gateway.Mode, false)
	w.kvString("bind", cfg.Gateway.Bind, false)
	w.kvInt("port", cfg.Gateway.Port)
	w.kvString("auth_mode", cfg.Gateway.AuthMode, false)
	w.kvString("auth_token_ref", cfg.Gateway.AuthToken, true)
	w.kvString("tailscale_mode", cfg.Gateway.TailscaleMode, false)
	w.kvString("control_ui_root", cfg.Gateway.ControlUIRoot, false)
	if cfg.Gateway.ControlUIAllowInsecure != nil {
		w.kvBool("control_ui_allow_insecure_auth", *cfg.Gateway.ControlUIAllowInsecure)
	}

	w.blank()
	w.section("agent")
	w.kvString("model", cfg.Agent.Model, false)
	w.kvStrings("fallback_models", cfg.Agent.Fallbacks)
	w.kvString("workspace", cfg.Agent.Workspace, false)
	w.kvString("tools_profile", cfg.Agent.ToolsProfile, false)
	w.kvInt("max_turns", cfg.Agent.MaxTurns)
	w.kvString("system_prompt", cfg.Agent.SystemPrompt, false)
	for _, alias := range cfg.Agent.ModelAliases {
		w.blank()
		w.arraySection("agent.model_aliases")
		w.kvString("model", alias.Model, false)
		w.kvString("alias", alias.Alias, false)
	}

	for _, sub := range cfg.Subagents {
		w.blank()
		w.arraySection("subagents")
		w.kvString("id", sub.ID, false)
		w.kvString("name", sub.Name, false)
		w.kvString("model", sub.Model, false)
		w.kvString("tools_profile", sub.ToolsProfile, false)
	}

	w.blank()
	w.section("memory")
	w.kvBool("enabled", cfg.Memory.Enabled)
	w.kvString("path", cfg.Memory.Path, false)
	w.kvString("model", cfg.Memory.Model, false)

	w.blank()
	w.section("tools")
	w.kvString("profile", cfg.Tools.Profile, false)
	if cfg.Tools.WebSearchEnabled != nil {
		w.kvBool("web_search_enabled", *cfg.Tools.WebSearchEnabled)
	}
	w.kvString("web_search_provider", cfg.Tools.WebSearchProvider, false)
	w.kvString("web_search_api_key_ref", cfg.Tools.WebSearchAPIKey, true)

	providerIDs := make([]string, 0, len(cfg.Models.Providers))
	for id := range cfg.Models.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		provider := cfg.Models.Providers[providerID]
		w.blank()
		w.section("models.providers." + tomlKey(providerID))
		w.kvString("api", provider.API, false)
		w.kvString("base_url", provider.BaseURL, false)
		w.kvString("api_key_ref", provider.APIKey, true)
		for _, model := range provider.Models {
			w.blank()
			w.arraySection("models.providers." + tomlKey(providerID) + ".models")
			w.kvString("id", model.ID, false)
			w.kvString("name", model.Name, false)
			w.kvString("api", model.API, false)
			w.kvInt("context_window", model.ContextWindow)
			w.kvInt("max_tokens", model.MaxTokens)
			w.kvStrings("input", model.Input)
			if model.Reasoning != nil {
				w.kvBool("reasoning", *model.Reasoning)
			}
			if model.Cost.hasAny() {
				w.cost(model.Cost)
			}
		}
	}

	w.blank()
	w.section("channels.telegram")
	w.kvBool("enabled", cfg.Telegram.Enabled)
	w.kvString("bot_token_ref", cfg.Telegram.BotToken, true)
	w.kvStrings("allow_from", cfg.Telegram.AllowFrom)
	w.kvString("agent_id", cfg.Telegram.AgentID, false)
	if cfg.Telegram.RequireMention != nil {
		w.kvBool("require_mention", *cfg.Telegram.RequireMention)
	}

	w.blank()
	w.section("plugins")
	w.kvStrings("enabled", cfg.Plugins.Enabled)
	w.kvStrings("deny", cfg.Plugins.Deny)
	w.kvStrings("load_paths", cfg.Plugins.LoadPaths)
	w.kvBool("legacy_openclaw_shim", cfg.Plugins.LegacyOpenClawShim)
	return []byte(b.String())
}

type tomlWriter struct {
	b    *strings.Builder
	opts MarshalOptions
}

func (w tomlWriter) comment(s string) { fmt.Fprintf(w.b, "# %s\n", s) }
func (w tomlWriter) blank()           { w.b.WriteByte('\n') }
func (w tomlWriter) section(name string) {
	fmt.Fprintf(w.b, "[%s]\n", name)
}
func (w tomlWriter) arraySection(name string) {
	fmt.Fprintf(w.b, "[[%s]]\n", name)
}
func (w tomlWriter) kvString(key, value string, secret bool) {
	if value == "" {
		return
	}
	if secret && w.opts.RedactSecrets && secretKind(value) == "literal" {
		value = "<redacted:literal>"
	}
	fmt.Fprintf(w.b, "%s = %s\n", key, strconv.Quote(value))
}
func (w tomlWriter) kvStrings(key string, values []string) {
	if len(values) == 0 {
		return
	}
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, strconv.Quote(v))
	}
	fmt.Fprintf(w.b, "%s = [%s]\n", key, strings.Join(quoted, ", "))
}
func (w tomlWriter) kvBool(key string, value bool) {
	fmt.Fprintf(w.b, "%s = %t\n", key, value)
}
func (w tomlWriter) kvInt(key string, value int64) {
	if value == 0 {
		return
	}
	fmt.Fprintf(w.b, "%s = %d\n", key, value)
}
func (c ModelCost) hasAny() bool {
	return c.Input != nil || c.Output != nil || c.CacheRead != nil || c.CacheWrite != nil
}

func (w tomlWriter) cost(cost ModelCost) {
	if cost.Input != nil {
		fmt.Fprintf(w.b, "cost_input = %s\n", formatFloat(*cost.Input))
	}
	if cost.Output != nil {
		fmt.Fprintf(w.b, "cost_output = %s\n", formatFloat(*cost.Output))
	}
	if cost.CacheRead != nil {
		fmt.Fprintf(w.b, "cost_cache_read = %s\n", formatFloat(*cost.CacheRead))
	}
	if cost.CacheWrite != nil {
		fmt.Fprintf(w.b, "cost_cache_write = %s\n", formatFloat(*cost.CacheWrite))
	}
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func tomlKey(key string) string {
	if key == "" {
		return strconv.Quote(key)
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return strconv.Quote(key)
	}
	return key
}
