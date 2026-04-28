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
	out := make([]Model, 0, len(openAI)+len(deepSeek))
	out = append(out, openAI...)
	out = append(out, deepSeek...)
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

	// --- 3.5 (cheap baseline) -----------------------------------------
	{
		Provider: "openai", ID: "gpt-3.5-turbo",
		Name: "GPT-3.5 Turbo", ContextWindow: 16385,
	},

	// --- reasoning (o-series) ------------------------------------------
	{
		Provider: "openai", ID: "o1",
		Name: "o1", ContextWindow: 200000, Reasoning: true,
	},
	{
		Provider: "openai", ID: "o1-mini",
		Name: "o1 mini", ContextWindow: 128000, Reasoning: true,
	},
	{
		Provider: "openai", ID: "o3-mini",
		Name: "o3 mini", ContextWindow: 200000, Reasoning: true,
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
}
