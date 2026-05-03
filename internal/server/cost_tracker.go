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
//   - models.<id>.priceUsdPer1M.in / .out: per-million-token price
//     in USD. When unset, falls back to a builtin price table for
//     well-known models. Unknown models with no price tracked at
//     0 cost (they don't contribute to the cap).
//
// Day rollover: time.Now().UTC().YearDay() comparison; flip the
// stored day stamp and zero the per-agent accumulator on mismatch.
// Restart wipes the accumulator (acceptable for v0 — the cap is
// for the "I left for a weekend" case, not invariant audit).

import (
	"sync"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/tidwall/gjson"
)

// CostTracker tallies per-agent USD spend per UTC day and refuses
// chat requests for agents that have hit the cap. Safe for
// concurrent use.
type CostTracker struct {
	paths openclaw.Paths

	mu    sync.Mutex
	today map[string]float64 // agentID → USD spent today
	day   int                // YearDay of `today`'s contents
}

// NewCostTracker constructs a tracker bound to paths. Reads the cap
// + price table from the merged config on each call (cheap thanks
// to the MergedBytes stat-keyed cache); avoids stale-config issues
// when the user updates limits without restarting the gateway.
func NewCostTracker(paths openclaw.Paths) *CostTracker {
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
	if t == nil {
		return
	}
	cap := t.dailyCap()
	if cap <= 0 {
		// No cap → don't bother tracking (saves the lock + map write).
		return
	}
	cost := t.costForUsage(model, u)
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
	in, out := t.priceFor(model)
	if in == 0 && out == 0 {
		return 0
	}
	// Prices are USD per 1M tokens.
	return float64(u.InputTokens)*in/1_000_000 + float64(u.OutputTokens)*out/1_000_000
}

// priceFor returns (inputUsdPer1M, outputUsdPer1M) for model,
// preferring config overrides at models.<id>.priceUsdPer1M.{in,out}
// over the builtin table. gjson treats "/" as a regular character
// in keys, so model ids like "deepseek/deepseek-chat" don't need
// escaping; "." in version segments would break path parsing but
// none of the models we ship use that today.
func (t *CostTracker) priceFor(model provider.ModelID) (inUSD, outUSD float64) {
	id := string(model)
	merged, err := config.MergedBytes(t.paths)
	if err == nil {
		base := "models." + id + ".priceUsdPer1M."
		if v := gjson.GetBytes(merged, base+"in"); v.Exists() {
			inUSD = v.Float()
		}
		if v := gjson.GetBytes(merged, base+"out"); v.Exists() {
			outUSD = v.Float()
		}
		if inUSD > 0 || outUSD > 0 {
			return
		}
	}
	if p, ok := builtinModelPrices[id]; ok {
		return p.In, p.Out
	}
	return 0, 0
}

// modelPrice is one entry in the builtin pricing table. USD per 1M
// tokens. Sourced from public pricing pages; rough enough for the
// cap-tripping use case (we're not invoicing customers, just
// preventing runaway burn).
type modelPrice struct {
	In  float64
	Out float64
}

// builtinModelPrices covers the providers/models we ship support
// for. Keep entries sparse: better to track at $0 than to lie
// about pricing of a model we haven't checked. Users override via
// config.
var builtinModelPrices = map[string]modelPrice{
	// Anthropic — claude-3.5/4 family approximations
	"anthropic/claude-opus-4-7":  {In: 15.0, Out: 75.0},
	"anthropic/claude-sonnet-4-6": {In: 3.0, Out: 15.0},
	// OpenAI
	"openai/gpt-4o":      {In: 2.5, Out: 10.0},
	"openai/gpt-4o-mini": {In: 0.15, Out: 0.60},
	"openai/gpt-3.5-turbo": {In: 0.5, Out: 1.5},
	// DeepSeek (deeply cheap)
	"deepseek/deepseek-chat":     {In: 0.27, Out: 1.10},
	"deepseek/deepseek-reasoner": {In: 0.55, Out: 2.19},
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
// zeros so $0.50 doesn't render as "$0.5000". Avoids pulling in
// strconv for one call by composing manually.
func ftoa(f float64) string {
	// Quick path: integer dollars.
	if f == float64(int64(f)) {
		buf := []byte{}
		buf = appendInt(buf, int64(f))
		return string(buf) + ".00"
	}
	// fmt.Sprintf would work but pulls in fmt; stay lightweight.
	cents := int64((f*10000)+0.5) // round to 4 decimals
	whole := cents / 10000
	frac := cents % 10000
	out := []byte{}
	out = appendInt(out, whole)
	out = append(out, '.')
	// pad fractional to 4 digits, then trim trailing zeros (but
	// keep at least 2 digits — "$1.50" beats "$1.5").
	digits := []byte{
		byte('0' + frac/1000%10),
		byte('0' + frac/100%10),
		byte('0' + frac/10%10),
		byte('0' + frac%10),
	}
	end := 4
	for end > 2 && digits[end-1] == '0' {
		end--
	}
	out = append(out, digits[:end]...)
	return string(out)
}

func appendInt(buf []byte, n int64) []byte {
	if n == 0 {
		return append(buf, '0')
	}
	if n < 0 {
		buf = append(buf, '-')
		n = -n
	}
	start := len(buf)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// Reverse the digits we just appended.
	for i, j := start, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return buf
}
