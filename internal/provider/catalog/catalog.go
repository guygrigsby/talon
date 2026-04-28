// Package catalog ships built-in model definitions for talon's
// natively-implemented providers. The data is the floor that
// models.list returns when the user hasn't filled out
// `models.providers.<provider>.models[]` in their openclaw.json
// — without it, a fresh install shows zero models and there's no
// way to pick one in the web UI.
//
// User config takes priority over catalog: when both define the same
// "<provider>/<id>" pair, the user's row replaces the built-in (so
// they can override e.g. contextWindow or add fields without losing
// the rest of the catalog).
//
// This package intentionally has zero dependencies on
// internal/provider so it can be imported anywhere (catalog readers
// don't need to instantiate providers). Adding a new model is
// data-only; new provider catalogs go via DefaultCatalog().
package catalog

// Model is one entry in the provider catalog. Field names mirror
// what handleModelsList already emits over the wire so this type
// can drop straight into the response.
type Model struct {
	// Provider is the provider key ("openai", "deepseek"). Required.
	Provider string

	// ID is the model id WITHOUT the provider segment (e.g. "gpt-4o",
	// not "openai/gpt-4o"). Required.
	ID string

	// Name is a human-readable label; empty falls back to ID in UIs.
	Name string

	// ContextWindow is the model's maximum input token capacity. 0
	// means "unspecified" — UIs should hide the column rather than
	// show a misleading zero.
	ContextWindow int64

	// Reasoning marks models that surface a thinking/reasoning step
	// (o1, o3-mini, deepseek-reasoner, …). The chat handler can use
	// this hint to suppress unsupported params like temperature.
	Reasoning bool

	// Input lists the input modalities the model accepts. ["text"] is
	// the implicit default; richer entries call out vision/audio/etc.
	Input []string
}

// DefaultCatalog returns the union of all provider catalogs. Result
// is a fresh slice on each call so callers can sort/filter without
// affecting the package state.
func DefaultCatalog() []Model {
	out := make([]Model, 0, len(openAI)+len(deepSeek)+len(lmStudio))
	out = append(out, openAI...)
	out = append(out, deepSeek...)
	out = append(out, lmStudio...)
	return out
}

// ForProvider returns just the entries belonging to providerName,
// in the same canonical order as DefaultCatalog. Useful when only
// one provider's catalog is needed (e.g. CLI auto-completion).
func ForProvider(providerName string) []Model {
	all := DefaultCatalog()
	out := make([]Model, 0, len(all))
	for _, m := range all {
		if m.Provider == providerName {
			out = append(out, m)
		}
	}
	return out
}

// LookupName returns the catalog's display name for a fully-qualified
// model id ("openai/gpt-4o"). Returns "" when the model isn't in the
// built-in catalog — callers should fall back to user-config or to
// the id itself for display. Cheap (linear scan; the catalog has
// dozens of entries, not thousands), called per agents.list.
func LookupName(fullID string) string {
	for _, m := range DefaultCatalog() {
		if m.Provider+"/"+m.ID == fullID {
			return m.Name
		}
	}
	return ""
}

// =============================================================================
// OpenAI
// =============================================================================
//
// Coverage strategy: the production-quality general-purpose chat
// models the UI's model picker is most likely to be used with.
// Skipped on purpose: realtime/audio-only variants, deprecated
// snapshots (gpt-4-0613 etc.), and embedding-only models — those
// belong in their own typed catalogs once those provider sub-types
// land (talon-578).

var openAI = []Model{
	// --- 4.1 family ----------------------------------------------------
	// Released April 2025; 1M context across the whole family. Default
	// pick for most chat workloads as of 2026.
	{
		Provider: "openai", ID: "gpt-4.1",
		Name: "GPT-4.1", ContextWindow: 1000000,
		Input: []string{"text", "image"},
	},
	{
		Provider: "openai", ID: "gpt-4.1-mini",
		Name: "GPT-4.1 mini", ContextWindow: 1000000,
		Input: []string{"text", "image"},
	},
	{
		Provider: "openai", ID: "gpt-4.1-nano",
		Name: "GPT-4.1 nano", ContextWindow: 1000000,
		Input: []string{"text", "image"},
	},

	// --- 4o family -----------------------------------------------------
	{
		Provider: "openai", ID: "gpt-4o",
		Name: "GPT-4o", ContextWindow: 128000,
		Input: []string{"text", "image"},
	},
	{
		Provider: "openai", ID: "gpt-4o-mini",
		Name: "GPT-4o mini", ContextWindow: 128000,
		Input: []string{"text", "image"},
	},
	{
		// Sticky alias OpenAI ships pointing at the latest GPT-4o
		// snapshot. Useful when you want "best 4o available" without
		// pinning a date-stamped revision.
		Provider: "openai", ID: "chatgpt-4o-latest",
		Name: "ChatGPT-4o (latest)", ContextWindow: 128000,
		Input: []string{"text", "image"},
	},

	// --- 4-turbo / classic 4 ------------------------------------------
	{
		Provider: "openai", ID: "gpt-4-turbo",
		Name: "GPT-4 Turbo", ContextWindow: 128000,
		Input: []string{"text", "image"},
	},
	{
		Provider: "openai", ID: "gpt-4",
		Name: "GPT-4", ContextWindow: 8192,
	},
	{
		Provider: "openai", ID: "gpt-4-32k",
		Name: "GPT-4 (32k)", ContextWindow: 32768,
	},

	// --- 3.5 (cheap baseline) -----------------------------------------
	{
		Provider: "openai", ID: "gpt-3.5-turbo",
		Name: "GPT-3.5 Turbo", ContextWindow: 16385,
	},

	// --- reasoning (o-series) ------------------------------------------
	// Reasoning=true → chat handler should suppress unsupported
	// params like temperature when the per-model capability flags
	// land (tracked separately).
	{
		Provider: "openai", ID: "o1",
		Name: "o1", ContextWindow: 200000, Reasoning: true,
		Input: []string{"text", "image"},
	},
	{
		Provider: "openai", ID: "o1-mini",
		Name: "o1 mini", ContextWindow: 128000, Reasoning: true,
	},
	{
		Provider: "openai", ID: "o1-pro",
		Name: "o1 pro", ContextWindow: 200000, Reasoning: true,
		Input: []string{"text", "image"},
	},
	{
		Provider: "openai", ID: "o3",
		Name: "o3", ContextWindow: 200000, Reasoning: true,
		Input: []string{"text", "image"},
	},
	{
		Provider: "openai", ID: "o3-mini",
		Name: "o3 mini", ContextWindow: 200000, Reasoning: true,
	},
	{
		Provider: "openai", ID: "o4-mini",
		Name: "o4 mini", ContextWindow: 200000, Reasoning: true,
		Input: []string{"text", "image"},
	},
}

// =============================================================================
// DeepSeek
// =============================================================================

var deepSeek = []Model{
	{
		Provider: "deepseek", ID: "deepseek-chat",
		Name: "DeepSeek Chat", ContextWindow: 64000,
	},
	{
		Provider: "deepseek", ID: "deepseek-reasoner",
		Name: "DeepSeek Reasoner", ContextWindow: 64000, Reasoning: true,
	},
	{
		Provider: "deepseek", ID: "deepseek-coder",
		Name: "DeepSeek Coder", ContextWindow: 16000,
	},
}

// =============================================================================
// LM Studio (local OpenAI-compatible server)
// =============================================================================
//
// Loaded models depend on what the user has running locally — we
// can't ship an authoritative list. Common picks below give a
// fresh-install picker something to point at; users override via
// `models.providers.lmstudio.models[]` when they swap weights.
//
// The id field must match LM Studio's "Loaded model" identifier
// (visible in LM Studio's server tab). LM Studio accepts whatever
// you load — these are illustrative defaults, not guarantees.

var lmStudio = []Model{
	{
		Provider: "lmstudio", ID: "llama-3.1-8b-instruct",
		Name: "Llama 3.1 8B Instruct (local)", ContextWindow: 128000,
	},
	{
		Provider: "lmstudio", ID: "qwen2.5-7b-instruct",
		Name: "Qwen 2.5 7B Instruct (local)", ContextWindow: 32768,
	},
	{
		Provider: "lmstudio", ID: "qwen2.5-coder-7b-instruct",
		Name: "Qwen 2.5 Coder 7B (local)", ContextWindow: 32768,
	},
	{
		Provider: "lmstudio", ID: "mistral-7b-instruct-v0.3",
		Name: "Mistral 7B Instruct v0.3 (local)", ContextWindow: 32768,
	},
}
