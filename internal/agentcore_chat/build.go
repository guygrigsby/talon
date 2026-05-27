package agentcore_chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guygrigsby/jess/memory"
	"github.com/tidwall/gjson"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/tools"

	"github.com/guygrigsby/talon/internal/agentcontext"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/guygrigsby/talon/internal/toolaccess"
)

// Builder assembles an `agentcore.Agent` from talon's merged config.
// Construction is split off the Handler so tests can build agents
// against fixture configs without going through the full chat-send
// entry point.
type Builder struct {
	merged []byte
	paths  talonpath.Paths
	// authOverride lets tests bypass secrets resolution. nil in
	// production; tests pass a fixed map to avoid touching the
	// real auth chain.
	authOverride    map[string]ProviderAuth
	selectedModelID string
	// memStore + memRecaller are optional. When both are non-nil
	// BuildAgent attaches jess RememberTool + RecallTool to the
	// agent. Built outside this package because chromem store
	// construction needs gomlx, which has heavy init cost — the
	// gateway builds the sidecar once and reuses across agents.
	memStore    memory.Store
	memRecaller memory.Recaller
	memKinds    *memory.KindRegistry
	memMaxItems int
	memHeader   string
	source      memory.Source
	// claudeIndex + claudeTool wire ADR 0013 read-only Claude-memory
	// access. claudeIndex (when non-empty) is folded into the system
	// prompt under a labeled section; claudeTool (when non-nil) is
	// appended to the tool set before toolaccess filtering, so the
	// per-agent tool policy governs it like any other tool. Set via
	// WithClaudeMemory; the gateway resolves both from native config.
	claudeIndex string
	claudeTool  agentcore.Tool
}

// NewBuilder constructs a Builder. merged is the result of
// `config.MergedBytes(paths)`; paths is needed for the auth
// resolver's profile-fallback step.
func NewBuilder(merged []byte, paths talonpath.Paths) *Builder {
	return &Builder{merged: merged, paths: paths}
}

// WithAuthOverride substitutes the resolved provider auth map.
// Test-only. Production callers leave this unset and let the
// resolver run.
func (b *Builder) WithAuthOverride(auth map[string]ProviderAuth) *Builder {
	b.authOverride = auth
	return b
}

// WithSelectedModel replaces the agent/default model for this build.
// The gateway uses this for the per-session model picker selection.
func (b *Builder) WithSelectedModel(modelID string) *Builder {
	b.selectedModelID = modelID
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

// WithMemoryOptions configures the jess memory ContextManager.
// Zero values leave jess defaults intact.
func (b *Builder) WithMemoryOptions(maxItems int, header string, kinds *memory.KindRegistry) *Builder {
	b.memMaxItems = maxItems
	b.memHeader = header
	b.memKinds = kinds
	return b
}

// WithClaudeMemory wires read-only Claude-memory access (ADR 0013).
// index is the capped MEMORY.md text to fold into the system prompt
// (empty = inject disabled / no index); tool is the claude_memory
// list/read tool registered subject to the agent's tool-access policy.
// The gateway resolves both from memory.claude.* native config.
func (b *Builder) WithClaudeMemory(index string, tool agentcore.Tool) *Builder {
	b.claudeIndex = index
	b.claudeTool = tool
	return b
}

// WithMemorySource stamps memories saved through the remember tool
// with Talon's session/run provenance.
func (b *Builder) WithMemorySource(sessionID, messageID string) *Builder {
	b.source.SessionID = sessionID
	b.source.MessageID = messageID
	return b
}

// BuildAgent assembles an `agentcore.Agent` for the named agent
// using the resolved model, provider auth, system prompt, and tool
// set. Returns the agent and the resolved ModelChoice (useful for
// telemetry — provider/model id at the call site without re-reading
// config).
func (b *Builder) BuildAgent(agentID string) (*agentcore.Agent, ModelChoice, error) {
	choice := ResolveModel(b.merged, agentID)
	if b.selectedModelID != "" {
		choice = ModelChoiceFromID(b.selectedModelID, choice.Fallbacks)
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

	// Persona dir: where IDENTITY/SOUL/etc. and the onboarding sentinel
	// live. The configured workspace, falling back to ~/.talon (the
	// main agent's default home) so identity loads even before a
	// workspace is set. Used for both the system prompt and onboarding.
	personaDir := resolvePersonaDir(workspace, b.paths.Talon.Dir)

	// System prompt: onboarding directive (when active) + workspace
	// persona files (IDENTITY/SOUL/AGENTS/USER) + configured
	// systemPrompt. Without persona the model has no identity and
	// hallucinates one (e.g. "I'm GPT-4").
	systemPrompt := buildSystemPrompt(b.merged, agentID, personaDir)

	// ADR 0013: fold the (capped) Claude-memory index into the system
	// prompt under a labeled section. Empty when injection is disabled
	// (memory.claude.inject=false) or no MEMORY.md exists.
	if b.claudeIndex != "" {
		systemPrompt += "\n\n## Notes Claude has saved about the user/project\n\n" + b.claudeIndex
	}

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
	// First-run onboarding: when the persona dir still has the
	// BOOTSTRAP sentinel, attach the finish_onboarding tool so the
	// agent can write its identity and clear the sentinel. Gated by
	// sentinel presence (only ever seeded in the main workspace), so
	// subagents never see it. Independent of workspace filesystem
	// tools — a fresh install has none of those.
	if agentcontext.BootstrapActive(personaDir) {
		toolSet = append(toolSet, newFinishOnboardingTool(personaDir))
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
	// ADR 0013: register the claude_memory tool before toolaccess
	// filtering so the per-agent tool policy governs it.
	if b.claudeTool != nil {
		toolSet = append(toolSet, b.claudeTool)
	}
	policy, err := toolaccess.Resolve(b.merged, b.paths, agentID)
	if err != nil {
		return nil, choice, fmt.Errorf("resolve tool access for %q: %w", agentID, err)
	}
	toolSet = filterAgentcoreTools(toolSet, policy)

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
	if cm := b.memoryContextManager(agentID); cm != nil {
		opts = append(opts, agentcore.WithContextManager(cm))
	}
	if b.source.SessionID != "" || b.source.MessageID != "" {
		opts = append(opts, agentcore.WithMiddlewares(memorySourceMiddleware(b.source)))
	}
	if systemPrompt != "" {
		opts = append(opts, agentcore.WithSystemPrompt(systemPrompt))
	}
	if len(toolSet) > 0 {
		opts = append(opts, agentcore.WithTools(toolSet...))
	}

	return agentcore.NewAgent(opts...), choice, nil
}

func filterAgentcoreTools(in []agentcore.Tool, policy toolaccess.Policy) []agentcore.Tool {
	if !policy.Enabled {
		return nil
	}
	if !policy.Restricted {
		return in
	}
	out := make([]agentcore.Tool, 0, len(in))
	for _, tool := range in {
		if policy.Allows(tool.Name()) {
			out = append(out, tool)
		}
	}
	return out
}

func (b *Builder) memoryContextManager(agentID string) agentcore.ContextManager {
	if b.memStore == nil || b.memRecaller == nil || agentID == "" {
		return nil
	}
	return memory.NewContextManager(b.memStore, b.memRecaller, memory.ContextManagerOptions{
		AgentID:  agentID,
		MaxItems: b.memMaxItems,
		Header:   b.memHeader,
		Kinds:    b.memKinds,
	})
}

func memorySourceMiddleware(src memory.Source) agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		if call.Name == "remember" {
			nextSrc := src
			nextSrc.Tool = call.Name
			if nextSrc.Reason == "" {
				nextSrc.Reason = "model decided"
			}
			ctx = memory.WithSource(ctx, nextSrc)
		}
		return next(ctx, call.Args)
	}
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

// resolvePersonaDir picks where persona files and the onboarding
// sentinel live: the configured workspace, falling back to talonDir
// (~/.talon) where the main agent's markdown lives by default. The
// fallback feeds identity/onboarding only — it never grants filesystem
// tools, which stay gated on a configured workspace.
func resolvePersonaDir(workspace, talonDir string) string {
	if workspace != "" {
		return workspace
	}
	return talonDir
}

// buildSystemPrompt composes, in priority order: the onboarding
// directive (when first-run onboarding is active), the workspace persona
// files (IDENTITY/SOUL/AGENTS/USER), and the configured systemPrompt.
// Any source may be empty.
func buildSystemPrompt(merged []byte, agentID, personaDir string) string {
	return composeSystemPrompt(
		agentcontext.BootstrapPrompt(personaDir),
		agentcontext.Build(personaDir),
		resolveSystemPrompt(merged, agentID),
	)
}

// composeSystemPrompt joins prompt sections in priority order, dropping
// empties and separating with a blank line. Onboarding leads (it must
// run first), then persona (who the agent is), then the configured
// prompt (supplemental instructions).
func composeSystemPrompt(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}
