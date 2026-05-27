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

// NativeConfig is the Talon-owned config shape.
type NativeConfig struct {
	Gateway  GatewayConfig   `mapstructure:"gateway"`
	Agent    ChatAgentConfig `mapstructure:"agent"`
	Memory   MemoryConfig    `mapstructure:"memory"`
	Tools    ToolsConfig     `mapstructure:"tools"`
	Models   ModelsConfig    `mapstructure:"models"`
	Telegram TelegramConfig  `mapstructure:"-"`
	Plugins  PluginsConfig   `mapstructure:"plugins"`
}

type GatewayConfig struct {
	Mode                   string `mapstructure:"mode"`
	Bind                   string `mapstructure:"bind"`
	Port                   int64  `mapstructure:"port"`
	AuthMode               string `mapstructure:"auth_mode"`
	AuthToken              string `mapstructure:"auth_token_ref"`
	AuthPassword           string `mapstructure:"auth_password_ref"`
	TailscaleMode          string `mapstructure:"tailscale_mode"`
	ControlUIRoot          string `mapstructure:"control_ui_root"`
	ControlUIAllowInsecure *bool  `mapstructure:"control_ui_allow_insecure_auth"`

	// Tailnet service bind (ADR 0008). Populated from gateway.tailscale.*.
	TailnetService       string `mapstructure:"-"` // e.g. "svc:talon"
	TailnetOAuthClientID string `mapstructure:"-"` // non-secret OAuth client id (plaintext)
	TailnetOAuthRef      string `mapstructure:"-"` // keychain://… or op://… ref to the OAuth secret
	TailnetName          string `mapstructure:"-"` // <tailnet>.ts.net, cached at provision
}

type ChatAgentConfig struct {
	Model        string       `mapstructure:"model"`
	Fallbacks    []string     `mapstructure:"fallback_models"`
	Workspace    string       `mapstructure:"workspace"`
	ToolsProfile string       `mapstructure:"tools_profile"`
	ToolsEnabled *bool        `mapstructure:"tools_enabled"`
	ModelAliases []ModelAlias `mapstructure:"model_aliases"`
	DailyUSDCap  float64      `mapstructure:"daily_usd_cap"`
	MaxTurns     int64        `mapstructure:"max_turns"`
	SystemPrompt string       `mapstructure:"system_prompt"`
}

type ModelAlias struct {
	Model string `mapstructure:"model"`
	Alias string `mapstructure:"alias"`
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
	DMPolicy       string   `mapstructure:"dm_policy"`
	AgentID        string   `mapstructure:"agent_id"`
	RequireMention *bool    `mapstructure:"require_mention"`
}

type PluginsConfig struct {
	Enabled   []string `mapstructure:"enabled"`
	Deny      []string `mapstructure:"deny"`
	LoadPaths []string `mapstructure:"load_paths"`
}

type MigrationReport struct {
	SourceTopLevelKeys []string
	StateCandidates    []string
	DropCandidates     []string
	SecretCounts       map[string]int
}

// FromOpenClawJSON converts an OpenClaw config JSON file into Talon's native
// shape. This is intentionally a migration-only entry point; runtime config
// reads use TOML.
func FromOpenClawJSON(raw []byte) (NativeConfig, MigrationReport, error) {
	return fromJSONConfig(raw, "OpenClaw config")
}

// FromRuntimeJSON converts Talon's current runtime JSON view into native
// TOML structs. It exists while older handlers still consume the JSON view.
func FromRuntimeJSON(raw []byte) (NativeConfig, error) {
	cfg, _, err := fromJSONConfig(raw, "runtime config")
	return cfg, err
}

func fromJSONConfig(raw []byte, sourceLabel string) (NativeConfig, MigrationReport, error) {
	if !gjson.ValidBytes(raw) {
		return NativeConfig{}, MigrationReport{}, fmt.Errorf("%s is not valid JSON", sourceLabel)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return NativeConfig{}, MigrationReport{}, err
	}

	var cfg NativeConfig
	cfg.Gateway = gatewayFromJSON(raw)
	cfg.Agent = chatAgentFromJSON(raw)
	cfg.Memory = memoryFromJSON(raw)
	cfg.Tools = toolsFromJSON(raw)
	cfg.Models = modelsFromJSON(raw)
	cfg.Telegram = telegramFromJSON(raw)
	cfg.Plugins = pluginsFromJSON(raw)

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
			"agents.list entries other than main; move them to ~/.talon/subagents/*.md",
			"agents.list[].agentDir",
			"pre-TOML workspace state files after TOML cutover",
			"old JSON config backups after TOML cutover",
		},
		SecretCounts: secretCounts(raw),
	}
	return cfg, report, nil
}

func gatewayFromJSON(raw []byte) GatewayConfig {
	g := GatewayConfig{
		Mode:          stringOr(raw, "gateway.mode", "local"),
		Bind:          stringOr(raw, "gateway.bind", "loopback"),
		Port:          intOr(raw, "gateway.port", 18789),
		AuthMode:      gjson.GetBytes(raw, "gateway.auth.mode").Str,
		AuthToken:     gjson.GetBytes(raw, "gateway.auth.token").Str,
		AuthPassword:  gjson.GetBytes(raw, "gateway.auth.password").Str,
		TailscaleMode: gjson.GetBytes(raw, "gateway.tailscale.mode").Str,
		ControlUIRoot: gjson.GetBytes(raw, "gateway.controlUi.root").Str,

		TailnetService:       gjson.GetBytes(raw, "gateway.tailscale.service").Str,
		TailnetOAuthClientID: gjson.GetBytes(raw, "gateway.tailscale.oauth_client_id").Str,
		TailnetOAuthRef:      gjson.GetBytes(raw, "gateway.tailscale.oauth_client_ref").Str,
		TailnetName:          gjson.GetBytes(raw, "gateway.tailscale.tailnet").Str,
	}
	if v := gjson.GetBytes(raw, "gateway.controlUi.allowInsecureAuth"); v.Exists() {
		b := v.Bool()
		g.ControlUIAllowInsecure = &b
	}
	return g
}

func chatAgentFromJSON(raw []byte) ChatAgentConfig {
	a := ChatAgentConfig{
		Model:        gjson.GetBytes(raw, "agents.defaults.model.primary").Str,
		Workspace:    normalizeMainWorkspace(gjson.GetBytes(raw, "agents.defaults.workspace").Str),
		ToolsProfile: gjson.GetBytes(raw, "tools.profile").Str,
		DailyUSDCap:  gjson.GetBytes(raw, "agents.defaults.dailyUsdCap").Float(),
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
			a.Workspace = normalizeMainWorkspace(workspace)
		}
		if profile := main.Get("tools.profile").Str; profile != "" {
			a.ToolsProfile = profile
		}
		if v := main.Get("tools.enabled"); v.Exists() {
			b := v.Bool()
			a.ToolsEnabled = &b
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

func normalizeMainWorkspace(workspace string) string {
	trimmed := strings.TrimRight(workspace, "/")
	if trimmed == "~/.talon/workspace" {
		return "~/.talon"
	}
	if strings.HasSuffix(trimmed, "/.talon/workspace") {
		return strings.TrimSuffix(trimmed, "/workspace")
	}
	return workspace
}

func memoryFromJSON(raw []byte) MemoryConfig {
	return MemoryConfig{
		Enabled: gjson.GetBytes(raw, "memory.enabled").Bool(),
		Path:    gjson.GetBytes(raw, "memory.path").Str,
		Model:   gjson.GetBytes(raw, "memory.model").Str,
	}
}

func toolsFromJSON(raw []byte) ToolsConfig {
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

func modelsFromJSON(raw []byte) ModelsConfig {
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
			p.Models = append(p.Models, modelFromJSON(m))
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
	gjson.GetBytes(raw, "models").ForEach(func(key, entry gjson.Result) bool {
		if key.Str == "providers" {
			return true
		}
		providerID, modelID, ok := strings.Cut(key.Str, "/")
		if !ok || providerID == "" || modelID == "" {
			return true
		}
		cost := costFromPriceOverride(entry.Get("priceUsdPer1M"))
		if !cost.hasAny() {
			return true
		}
		if p := ensure(providerID); p != nil {
			applyCostOverride(p, modelID, cost)
		}
		return true
	})

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

func applyCostOverride(provider *ModelProviderConfig, modelID string, cost ModelCost) {
	if provider == nil || modelID == "" || !cost.hasAny() {
		return
	}
	for i := range provider.Models {
		if provider.Models[i].ID != modelID {
			continue
		}
		mergeModelCost(&provider.Models[i].Cost, cost)
		return
	}
	provider.Models = append(provider.Models, ModelConfig{ID: modelID, Cost: cost})
}

func mergeModelCost(dst *ModelCost, src ModelCost) {
	if src.Input != nil {
		dst.Input = src.Input
	}
	if src.Output != nil {
		dst.Output = src.Output
	}
	if src.CacheRead != nil {
		dst.CacheRead = src.CacheRead
	}
	if src.CacheWrite != nil {
		dst.CacheWrite = src.CacheWrite
	}
}

func modelFromJSON(m gjson.Result) ModelConfig {
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
		out.Cost = costFromJSON(cost)
	}
	return out
}

func costFromPriceOverride(price gjson.Result) ModelCost {
	var out ModelCost
	if !price.Exists() {
		return out
	}
	if v := price.Get("in"); v.Exists() {
		f := v.Float()
		out.Input = &f
	}
	if v := price.Get("out"); v.Exists() {
		f := v.Float()
		out.Output = &f
	}
	if v := price.Get("cacheRead"); v.Exists() {
		f := v.Float()
		out.CacheRead = &f
	}
	if v := price.Get("cacheWrite"); v.Exists() {
		f := v.Float()
		out.CacheWrite = &f
	}
	return out
}

func costFromJSON(cost gjson.Result) ModelCost {
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

func telegramFromJSON(raw []byte) TelegramConfig {
	t := TelegramConfig{
		Enabled:   gjson.GetBytes(raw, "channels.telegram.enabled").Bool(),
		BotToken:  gjson.GetBytes(raw, "channels.telegram.botToken").Str,
		AllowFrom: stringArray(gjson.GetBytes(raw, "channels.telegram.allowFrom")),
		DMPolicy:  gjson.GetBytes(raw, "channels.telegram.dmPolicy").Str,
		AgentID:   gjson.GetBytes(raw, "channels.telegram.agentId").Str,
	}
	if v := gjson.GetBytes(raw, "channels.telegram.groups.*.requireMention"); v.Exists() {
		b := v.Bool()
		t.RequireMention = &b
	}
	return t
}

func pluginsFromJSON(raw []byte) PluginsConfig {
	var p PluginsConfig
	gjson.GetBytes(raw, "plugins.entries").ForEach(func(name, entry gjson.Result) bool {
		if entry.Get("enabled").Bool() {
			p.Enabled = append(p.Enabled, name.Str)
		}
		return true
	})
	p.Deny = stringArray(gjson.GetBytes(raw, "plugins.deny"))
	p.LoadPaths = stringArray(gjson.GetBytes(raw, "plugins.load.paths"))
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

// ToRuntimeJSON adapts native TOML config into the JSON view consumed by the
// gateway handlers. fallbackRuntime preserves fields that do not yet have
// native TOML structs, and lets redacted native literals resolve from the
// previous runtime view during config edits.
func ToRuntimeJSON(cfg NativeConfig, fallbackRuntime []byte) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(fallbackRuntime)) > 0 {
		if err := json.Unmarshal(fallbackRuntime, &root); err != nil {
			return nil, fmt.Errorf("parse fallback runtime config: %w", err)
		}
	}

	applyGatewayRuntime(root, cfg, fallbackRuntime)
	applyAgentsRuntime(root, cfg)
	applyMemoryRuntime(root, cfg)
	applyToolsRuntime(root, cfg, fallbackRuntime)
	applyModelsRuntime(root, cfg, fallbackRuntime)
	applyTelegramRuntime(root, cfg, fallbackRuntime)
	applyPluginsRuntime(root, cfg)

	return json.Marshal(root)
}

func applyGatewayRuntime(root map[string]any, cfg NativeConfig, fallback []byte) {
	setString(root, cfg.Gateway.Mode, "gateway", "mode")
	setString(root, cfg.Gateway.Bind, "gateway", "bind")
	setInt(root, cfg.Gateway.Port, "gateway", "port")
	setString(root, cfg.Gateway.AuthMode, "gateway", "auth", "mode")
	if token := usableSecret(cfg.Gateway.AuthToken, fallbackString(fallback, "gateway.auth.token")); token != "" {
		setPath(root, token, "gateway", "auth", "token")
	}
	if password := usableSecret(cfg.Gateway.AuthPassword, fallbackString(fallback, "gateway.auth.password")); password != "" {
		setPath(root, password, "gateway", "auth", "password")
	}
	setString(root, cfg.Gateway.TailscaleMode, "gateway", "tailscale", "mode")
	setString(root, cfg.Gateway.ControlUIRoot, "gateway", "controlUi", "root")
	if cfg.Gateway.ControlUIAllowInsecure != nil {
		setPath(root, *cfg.Gateway.ControlUIAllowInsecure, "gateway", "controlUi", "allowInsecureAuth")
	}
}

func applyAgentsRuntime(root map[string]any, cfg NativeConfig) {
	setString(root, cfg.Agent.Model, "agents", "defaults", "model", "primary")
	setStrings(root, cfg.Agent.Fallbacks, "agents", "defaults", "model", "fallbacks")
	setString(root, cfg.Agent.Workspace, "agents", "defaults", "workspace")
	setString(root, cfg.Agent.ToolsProfile, "agents", "defaults", "tools", "profile")
	setFloat(root, cfg.Agent.DailyUSDCap, "agents", "defaults", "dailyUsdCap")
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

	agents := make([]any, 0, 1)
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
	if cfg.Agent.ToolsEnabled != nil {
		tools, _ := main["tools"].(map[string]any)
		if tools == nil {
			tools = map[string]any{}
		}
		tools["enabled"] = *cfg.Agent.ToolsEnabled
		main["tools"] = tools
	}
	if cfg.Agent.MaxTurns > 0 {
		main["maxTurns"] = cfg.Agent.MaxTurns
	}
	if cfg.Agent.SystemPrompt != "" {
		main["systemPrompt"] = cfg.Agent.SystemPrompt
	}
	agents = append(agents, main)
	setPath(root, agents, "agents", "list")
}

func applyMemoryRuntime(root map[string]any, cfg NativeConfig) {
	setPath(root, cfg.Memory.Enabled, "memory", "enabled")
	setString(root, cfg.Memory.Path, "memory", "path")
	setString(root, cfg.Memory.Model, "memory", "model")
}

func applyToolsRuntime(root map[string]any, cfg NativeConfig, fallback []byte) {
	setString(root, cfg.Tools.Profile, "tools", "profile")
	if cfg.Tools.WebSearchEnabled != nil {
		setPath(root, *cfg.Tools.WebSearchEnabled, "tools", "web", "search", "enabled")
	}
	setString(root, cfg.Tools.WebSearchProvider, "tools", "web", "search", "provider")
	if key := usableSecret(cfg.Tools.WebSearchAPIKey, fallbackString(fallback, "plugins.entries.brave.config.webSearch.apiKey")); key != "" {
		setPath(root, key, "plugins", "entries", "brave", "config", "webSearch", "apiKey")
	}
}

func applyModelsRuntime(root map[string]any, cfg NativeConfig, fallback []byte) {
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
				models = append(models, modelToRuntime(model))
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

func applyTelegramRuntime(root map[string]any, cfg NativeConfig, fallback []byte) {
	setPath(root, cfg.Telegram.Enabled, "channels", "telegram", "enabled")
	if token := usableSecret(cfg.Telegram.BotToken, fallbackString(fallback, "channels.telegram.botToken")); token != "" {
		setPath(root, token, "channels", "telegram", "botToken")
	}
	setStrings(root, cfg.Telegram.AllowFrom, "channels", "telegram", "allowFrom")
	setString(root, cfg.Telegram.DMPolicy, "channels", "telegram", "dmPolicy")
	setString(root, cfg.Telegram.AgentID, "channels", "telegram", "agentId")
	if cfg.Telegram.RequireMention != nil {
		setPath(root, *cfg.Telegram.RequireMention, "channels", "telegram", "groups", "*", "requireMention")
	}
}

func applyPluginsRuntime(root map[string]any, cfg NativeConfig) {
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

func modelToRuntime(model ModelConfig) map[string]any {
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

func setFloat(root map[string]any, value float64, path ...string) {
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
	w.comment("Plaintext secrets are omitted; move them to 1Password or the OS keychain and store refs here.")
	w.comment("The main chat agent keeps its workspace Markdown files; subagents live in ~/.talon/subagents/*.md.")
	w.blank()
	w.section("gateway")
	w.kvString("mode", cfg.Gateway.Mode, false)
	w.kvString("bind", cfg.Gateway.Bind, false)
	w.kvInt("port", cfg.Gateway.Port)
	w.kvString("auth_mode", cfg.Gateway.AuthMode, false)
	w.kvString("auth_token_ref", cfg.Gateway.AuthToken, true)
	w.kvString("auth_password_ref", cfg.Gateway.AuthPassword, true)
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
	if cfg.Agent.ToolsEnabled != nil {
		w.kvBool("tools_enabled", *cfg.Agent.ToolsEnabled)
	}
	w.kvFloat("daily_usd_cap", cfg.Agent.DailyUSDCap)
	w.kvInt("max_turns", cfg.Agent.MaxTurns)
	w.kvString("system_prompt", cfg.Agent.SystemPrompt, false)
	for _, alias := range cfg.Agent.ModelAliases {
		w.blank()
		w.arraySection("agent.model_aliases")
		w.kvString("model", alias.Model, false)
		w.kvString("alias", alias.Alias, false)
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
	w.kvString("dm_policy", cfg.Telegram.DMPolicy, false)
	w.kvString("agent_id", cfg.Telegram.AgentID, false)
	if cfg.Telegram.RequireMention != nil {
		w.kvBool("require_mention", *cfg.Telegram.RequireMention)
	}

	w.blank()
	w.section("plugins")
	w.kvStrings("enabled", cfg.Plugins.Enabled)
	w.kvStrings("deny", cfg.Plugins.Deny)
	w.kvStrings("load_paths", cfg.Plugins.LoadPaths)
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
		w.comment(fmt.Sprintf("%s omitted: plaintext secrets must be moved to 1Password or the OS keychain", key))
		return
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
func (w tomlWriter) kvFloat(key string, value float64) {
	if value == 0 {
		return
	}
	fmt.Fprintf(w.b, "%s = %s\n", key, formatFloat(value))
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
