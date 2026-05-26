package server

// Daily cost tracker. Per-agent USD accumulator with a config-
// driven cap that lets the user leave talon running unattended
// without worrying that a runaway loop will burn the credit card.
//
// Hot path: chat iterations call Allow() before sending the
// provider request and Record() after the DeltaUsage event lands.
// Both are O(1) (lock + map access) so the perf overhead is in the
// noise vs. provider latency.
//
// Config inputs:
//   - agents.defaults.dailyUsdCap: USD per agent per UTC day. 0 or
//     unset disables the cap (no tracking either, save the work).
//   - models.providers.<provider>.models[].cost: per-million-token
//     price in USD. Legacy models.<id>.priceUsdPer1M.{in,out} is
//     still honored before TOML adaptation. When unset, falls back
//     to a builtin price table for well-known models. Unknown models
//     with no price tracked at 0 cost (they don't contribute to the
//     cap).
//
// Day rollover: time.Now().UTC().YearDay() comparison; flip the
// stored day stamp and zero the per-agent accumulator on mismatch.
// Restart wipes the accumulator (acceptable for v0 — the cap is
// for the "I left for a weekend" case, not invariant audit).

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

// CostTracker tallies per-agent USD spend per UTC day and refuses
// chat requests for agents that have hit the cap. Safe for
// concurrent use.
type CostTracker struct {
	paths talonpath.Paths

	mu    sync.Mutex
	today map[string]float64 // agentID → USD spent today
	day   int                // YearDay of `today`'s contents
}

// NewCostTracker constructs a tracker bound to paths. Reads the cap
// + price table from the merged config on each call (cheap thanks
// to the MergedBytes stat-keyed cache); avoids stale-config issues
// when the user updates limits without restarting the gateway.
func NewCostTracker(paths talonpath.Paths) *CostTracker {
	return &CostTracker{
		paths: paths,
		today: map[string]float64{},
		day:   time.Now().UTC().YearDay(),
	}
}

// Allow returns nil when agentID is permitted to start a new chat
// iteration today; non-nil with a user-facing message when the cap
// has been hit. Cap=0 (unset) always returns nil.
func (t *CostTracker) Allow(agentID string) error {
	if t == nil {
		return nil
	}
	cap := t.dailyCap()
	if cap <= 0 {
		return nil
	}
	t.mu.Lock()
	t.rollIfNewDay()
	spent := t.today[agentID]
	t.mu.Unlock()
	if spent >= cap {
		return &costCapError{
			AgentID: agentID,
			SpentUS: spent,
			CapUSD:  cap,
		}
	}
	return nil
}

// Record adds the USD cost of one provider response (computed from
// usage tokens × the model's per-token price) to today's spend.
// Caller passes the model used so we can look up pricing.
func (t *CostTracker) Record(agentID string, model provider.ModelID, u provider.Usage) {
	t.RecordUsage(agentID, string(model), u.InputTokens, u.OutputTokens)
}

// RecordUsage adds the USD cost for one response using Talon's
// canonical model id string. This is the provider-neutral entry
// point used by the agentcore path.
func (t *CostTracker) RecordUsage(agentID, modelID string, inputTokens, outputTokens int) {
	if t == nil {
		return
	}
	cap := t.dailyCap()
	if cap <= 0 {
		// No cap → don't bother tracking (saves the lock + map write).
		return
	}
	cost := t.costForTokens(modelID, inputTokens, outputTokens)
	if cost <= 0 {
		return
	}
	t.mu.Lock()
	t.rollIfNewDay()
	t.today[agentID] += cost
	t.mu.Unlock()
}

// rollIfNewDay zeros the accumulator when the UTC day has rolled
// over since the last update. Caller holds t.mu.
func (t *CostTracker) rollIfNewDay() {
	d := time.Now().UTC().YearDay()
	if d == t.day {
		return
	}
	t.day = d
	for k := range t.today {
		delete(t.today, k)
	}
}

// dailyCap reads the configured cap. Returns 0 (no cap) when unset
// or the merged config can't be read — failure-open is the right
// default; refusing chat just because we couldn't read the config
// would be worse than letting it run.
func (t *CostTracker) dailyCap() float64 {
	merged, err := config.MergedBytes(t.paths)
	if err != nil {
		return 0
	}
	v := gjson.GetBytes(merged, "agents.defaults.dailyUsdCap")
	if !v.Exists() {
		return 0
	}
	return v.Float()
}

// costForUsage computes USD for one Usage record. Looks up the
// per-token price for model from the config first, then the
// builtin table; returns 0 when neither is available.
func (t *CostTracker) costForUsage(model provider.ModelID, u provider.Usage) float64 {
	return t.costForTokens(string(model), u.InputTokens, u.OutputTokens)
}

func (t *CostTracker) costForTokens(modelID string, inputTokens, outputTokens int) float64 {
	model := provider.ModelID(modelID)
	in, out := t.priceFor(model)
	if in == 0 && out == 0 {
		return 0
	}
	// Prices are USD per 1M tokens.
	return float64(inputTokens)*in/1_000_000 + float64(outputTokens)*out/1_000_000
}

// priceFor returns (inputUsdPer1M, outputUsdPer1M) for model,
// preferring config model costs over the builtin table.
func (t *CostTracker) priceFor(model provider.ModelID) (inUSD, outUSD float64) {
	id := string(model)
	merged, err := config.MergedBytes(t.paths)
	if err == nil {
		if p, ok := configuredModelPrice(merged, id); ok {
			return p.In, p.Out
		}
		if p, ok := configuredProviderModelPrice(merged, id); ok {
			return p.In, p.Out
		}
	}
	if p, ok := builtinModelPrices[id]; ok {
		return p.In, p.Out
	}
	return 0, 0
}

func configuredModelPrice(merged []byte, modelKey string) (modelPrice, bool) {
	var out modelPrice
	found := false
	gjson.GetBytes(merged, "models").ForEach(func(k, v gjson.Result) bool {
		if k.Str != modelKey {
			return true
		}
		price := v.Get("priceUsdPer1M")
		if !price.Exists() {
			return false
		}
		if in := price.Get("in"); in.Exists() {
			out.In = in.Float()
		}
		if outPrice := price.Get("out"); outPrice.Exists() {
			out.Out = outPrice.Float()
		}
		found = true
		return false
	})
	return out, found && (out.In > 0 || out.Out > 0)
}

func configuredProviderModelPrice(merged []byte, modelKey string) (modelPrice, bool) {
	var out modelPrice
	providerID, modelID, ok := strings.Cut(modelKey, "/")
	if !ok || providerID == "" || modelID == "" {
		return modelPrice{}, false
	}
	q := fmt.Sprintf("models.providers.%s.models.#(id==%q).cost", providerID, modelID)
	cost := gjson.GetBytes(merged, q)
	if !cost.Exists() {
		return modelPrice{}, false
	}
	copyPriceField(&out.In, cost.Get("input"))
	copyPriceField(&out.Out, cost.Get("output"))
	copyPriceField(&out.CacheRead, cost.Get("cacheRead"))
	return out, out.In > 0 || out.Out > 0 || out.CacheRead > 0
}

func copyPriceField(dst *float64, v gjson.Result) {
	if v.Exists() && v.Type == gjson.Number {
		*dst = v.Float()
	}
}

// modelPrice is one entry in the builtin pricing table. USD per 1M
// tokens. Sourced from public pricing pages; rough enough for the
// cap-tripping use case (we're not invoicing customers, just
// preventing runaway burn).
type modelPrice struct {
	In        float64
	Out       float64
	CacheRead float64
}

// builtinModelPrices covers the providers/models we ship support
// for. Keep entries sparse: better to track at $0 than to lie
// about pricing of a model we haven't checked. Users override via
// config.
var builtinModelPrices = map[string]modelPrice{
	// OpenAI standard rates, USD per 1M tokens.
	"openai/gpt-5.5":       {In: 5.0, Out: 30.0, CacheRead: 0.50},
	"openai/gpt-5.5-pro":   {In: 30.0, Out: 180.0},
	"openai/gpt-5.4":       {In: 2.5, Out: 15.0, CacheRead: 0.25},
	"openai/gpt-5.4-mini":  {In: 0.75, Out: 4.50, CacheRead: 0.075},
	"openai/gpt-5.4-nano":  {In: 0.20, Out: 1.25, CacheRead: 0.02},
	"openai/gpt-5.4-pro":   {In: 30.0, Out: 180.0},
	"openai/gpt-5":         {In: 1.25, Out: 10.0, CacheRead: 0.125},
	"openai/gpt-5-mini":    {In: 0.25, Out: 2.0, CacheRead: 0.025},
	"openai/gpt-5-nano":    {In: 0.05, Out: 0.40, CacheRead: 0.005},
	"openai/gpt-4o":        {In: 2.5, Out: 10.0},
	"openai/gpt-4o-mini":   {In: 0.15, Out: 0.60},
	"openai/gpt-4.1":       {In: 2.0, Out: 8.0},
	"openai/gpt-4.1-mini":  {In: 0.40, Out: 1.60},
	"openai/gpt-4.1-nano":  {In: 0.10, Out: 0.40},
	"openai/gpt-3.5-turbo": {In: 0.5, Out: 1.5},

	// Anthropic first-party Claude API rates, USD per 1M tokens.
	"anthropic/claude-opus-4-7":   {In: 5.0, Out: 25.0, CacheRead: 0.50},
	"anthropic/claude-opus-4-6":   {In: 5.0, Out: 25.0, CacheRead: 0.50},
	"anthropic/claude-opus-4-5":   {In: 5.0, Out: 25.0, CacheRead: 0.50},
	"anthropic/claude-opus-4-1":   {In: 15.0, Out: 75.0, CacheRead: 1.50},
	"anthropic/claude-opus-4":     {In: 15.0, Out: 75.0, CacheRead: 1.50},
	"anthropic/claude-sonnet-4-6": {In: 3.0, Out: 15.0, CacheRead: 0.30},
	"anthropic/claude-sonnet-4-5": {In: 3.0, Out: 15.0, CacheRead: 0.30},
	"anthropic/claude-sonnet-4":   {In: 3.0, Out: 15.0, CacheRead: 0.30},
	"anthropic/claude-haiku-4-5":  {In: 1.0, Out: 5.0, CacheRead: 0.10},

	// DeepSeek API rates, USD per 1M tokens.
	"deepseek/deepseek-chat":     {In: 0.27, Out: 1.10, CacheRead: 0.07},
	"deepseek/deepseek-reasoner": {In: 0.55, Out: 2.19, CacheRead: 0.14},
}

// costCapError is the error type Allow returns when an agent has
// hit its daily cap. Implements error so chat handlers can surface
// it to the model with full detail.
type costCapError struct {
	AgentID string
	SpentUS float64
	CapUSD  float64
}

func (e *costCapError) Error() string {
	return "daily USD cap reached for agent " + e.AgentID +
		" (spent $" + ftoa(e.SpentUS) + " of $" + ftoa(e.CapUSD) +
		"); resets at next UTC midnight, or raise agents.defaults.dailyUsdCap"
}

// ftoa formats a USD value with 4 decimal places, trimming trailing
// zeros so $0.50 doesn't render as "$0.5000".
func ftoa(f float64) string {
	s := strconv.FormatFloat(f, 'f', 4, 64)
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		for len(s)-dot-1 > 2 && strings.HasSuffix(s, "0") {
			s = s[:len(s)-1]
		}
	}
	return s
}
