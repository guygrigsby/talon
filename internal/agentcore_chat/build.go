package agentcore_chat

import (
	"fmt"

	"github.com/guygrigsby/jess/memory"
	"github.com/tidwall/gjson"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/tools"

	"github.com/guygrigsby/talon/internal/openclaw"
)

// Builder assembles an `agentcore.Agent` from talon's merged config.
// Construction is split off the Handler so tests can build agents
// against fixture configs without going through the full chat-send
// entry point.
type Builder struct {
	merged []byte
	paths  openclaw.Paths
	// authOverride lets tests bypass secrets resolution. nil in
	// production; tests pass a fixed map to avoid touching the
	// real auth chain.
	authOverride  map[string]ProviderAuth
	modelOverride string
	// memStore + memRecaller are optional. When both are non-nil
	// BuildAgent attaches jess RememberTool + RecallTool to the
	// agent. Built outside this package because chromem store
	// construction needs gomlx, which has heavy init cost — the
	// gateway builds the sidecar once and reuses across agents.
	memStore    memory.Store
	memRecaller memory.Recaller
}

// NewBuilder constructs a Builder. merged is the result of
// `config.MergedBytes(paths)`; paths is needed for the auth
// resolver's profile-fallback step.
func NewBuilder(merged []byte, paths openclaw.Paths) *Builder {
	return &Builder{merged: merged, paths: paths}
}

// WithAuthOverride substitutes the resolved provider auth map.
// Test-only. Production callers leave this unset and let the
// resolver run.
func (b *Builder) WithAuthOverride(auth map[string]ProviderAuth) *Builder {
	b.authOverride = auth
	return b
}

// WithModelOverride replaces the agent/default model for this build.
// The gateway uses this for per-session model picker overrides.
func (b *Builder) WithModelOverride(modelID string) *Builder {
	b.modelOverride = modelID
	return b
}

// WithMemory wires jess memory tools (RememberTool + RecallTool)
// into the agent. The store + recaller are typically built once by
// the gateway via gateway_memory.go and reused across builds.
// Nil values disable memory tooling (no recall, no save).
func (b *Builder) WithMemory(store memory.Store, recaller memory.Recaller) *Builder {
	b.memStore = store
	b.memRecaller = recaller
	return b
}

// BuildAgent assembles an `agentcore.Agent` for the named agent
// using the resolved model, provider auth, system prompt, and tool
// set. Returns the agent and the resolved ModelChoice (useful for
// telemetry — provider/model id at the call site without re-reading
// config).
func (b *Builder) BuildAgent(agentID string) (*agentcore.Agent, ModelChoice, error) {
	choice := ResolveModel(b.merged, agentID)
	if b.modelOverride != "" {
		choice = ModelChoiceFromID(b.modelOverride, choice.Fallbacks)
	}
	if choice.ID == "" {
		return nil, choice, fmt.Errorf("no model configured for agent %q (set agents.list[].model.primary or agents.defaults.model.primary)", agentID)
	}
	if choice.Provider == "" {
		return nil, choice, fmt.Errorf("model %q has no provider segment (expected '<provider>/<model>')", choice.ID)
	}

	auth := b.authOverride
	if auth == nil {
		auth = ResolveProviderAuth(b.merged, b.paths)
	}
	pa, ok := auth[choice.Provider]
	if !ok {
		return nil, choice, fmt.Errorf("provider %q is unauthenticated — configure plugins.entries.openai-compat.config.providers.%s.apiKey (or plugins.entries.anthropic.config.apiKey for anthropic)", choice.Provider, choice.Provider)
	}

	apiKey := pa.APIKey
	if apiKey == "" && isLoopbackURL(pa.BaseURL) {
		// LiteLLM's base provider validates that APIKey is non-empty
		// regardless of whether the upstream actually checks it. For
		// local servers (LM Studio, MLX, Ollama) that don't require
		// auth, a placeholder satisfies the validation and the local
		// server ignores it.
		apiKey = "no-auth"
	}
	modelOpts := []llm.ModelOption{}
	if apiKey != "" {
		modelOpts = append(modelOpts, llm.WithAPIKey(apiKey))
	}
	if pa.BaseURL != "" {
		modelOpts = append(modelOpts, llm.WithBaseURL(pa.BaseURL))
	}
	rawModel, err := llm.NewModel(choice.Provider, choice.Model, modelOpts...)
	if err != nil {
		return nil, choice, fmt.Errorf("init model %s/%s: %w", choice.Provider, choice.Model, err)
	}
	// Cap max_tokens per-model. agentcore.llm.DefaultGenerationConfig
	// hard-codes 65536, which 400s on every model with a smaller cap
	// (gpt-4o-mini = 16384, etc.). See model_cap.go.
	//
	// Resolution order: user-configured per-model `maxTokens` →
	// safe default that works on every known production model
	// (4096). Users wanting a higher cap set
	// models.providers.<provider>.models[id==<model>].maxTokens.
	cap := resolveModelMaxTokens(b.merged, choice.Provider, choice.Model)
	if cap <= 0 {
		cap = 4096
	}
	model := newCappedChatModel(rawModel, cap)

	// Workspace: per-agent workspace (agents.list[].workspace) or
	// defaults (agents.defaults.workspace). Tools confine paths
	// here.
	workspace := resolveAgentWorkspace(b.merged, agentID)

	// System prompt: agents.list[].systemPrompt or
	// agents.defaults.systemPrompt. Empty is fine.
	systemPrompt := resolveSystemPrompt(b.merged, agentID)

	// File-state shared across read/write/edit so write-after-read
	// invariants hold.
	fileState := tools.NewFileReadState()

	toolSet := []agentcore.Tool{}
	if workspace != "" {
		toolSet = append(toolSet,
			tools.NewRead(workspace, fileState),
			tools.NewWrite(workspace, fileState),
			tools.NewEdit(workspace, fileState),
			tools.NewBash(workspace),
			tools.NewGlob(workspace),
			tools.NewGrep(workspace),
			tools.NewLs(workspace),
		)
	}
	// jess memory tools. Attached when WithMemory was set AND the
	// agent has a non-empty id (jess refuses construction with
	// AgentID==""). Recall requires both store + recaller.
	if b.memStore != nil && agentID != "" {
		toolSet = append(toolSet, memory.NewRememberTool(b.memStore, memory.RememberOptions{AgentID: agentID}))
		if b.memRecaller != nil {
			toolSet = append(toolSet, memory.NewRecallTool(b.memStore, b.memRecaller, memory.RecallOptions{AgentID: agentID}))
		}
	}

	maxTurns := int(gjson.GetBytes(b.merged, "agents.defaults.maxTurns").Int())
	if v := gjson.GetBytes(b.merged, fmt.Sprintf("agents.list.#(id==%q).maxTurns", agentID)).Int(); v > 0 {
		maxTurns = int(v)
	}
	if maxTurns <= 0 {
		maxTurns = 20
	}

	opts := []agentcore.AgentOption{
		agentcore.WithModel(model),
		agentcore.WithMaxTurns(maxTurns),
	}
	if systemPrompt != "" {
		opts = append(opts, agentcore.WithSystemPrompt(systemPrompt))
	}
	if len(toolSet) > 0 {
		opts = append(opts, agentcore.WithTools(toolSet...))
	}

	return agentcore.NewAgent(opts...), choice, nil
}

// resolveAgentWorkspace mirrors the existing chat handler's lookup.
// Falls back to the talon overlay's default workspace dir.
func resolveAgentWorkspace(merged []byte, agentID string) string {
	if v := gjson.GetBytes(merged, fmt.Sprintf("agents.list.#(id==%q).workspace", agentID)).Str; v != "" {
		return v
	}
	return gjson.GetBytes(merged, "agents.defaults.workspace").Str
}

// resolveSystemPrompt picks a per-agent system prompt or falls back
// to the defaults. Returns "" when neither is set — the agent will
// use agentcore's default (no system prompt).
func resolveSystemPrompt(merged []byte, agentID string) string {
	if v := gjson.GetBytes(merged, fmt.Sprintf("agents.list.#(id==%q).systemPrompt", agentID)).Str; v != "" {
		return v
	}
	return gjson.GetBytes(merged, "agents.defaults.systemPrompt").Str
}
