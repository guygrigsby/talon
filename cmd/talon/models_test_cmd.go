// `talon models test` — per-model smoke probe. Sweeps every model
// configured under models.providers.<name>.models[] and fires a
// tiny "reply with ok" prompt against each, measuring TTFB and
// total latency, capturing any error.
//
// Bypasses the plugin subprocess on purpose: this command targets
// each provider's chat-completions endpoint directly using the
// in-tree openai/anthropic packages, so the numbers attribute to
// provider/network only (not plugin gRPC hop, not memory recall,
// not chat-loop overhead). Use it to answer "which models are
// slow / broken / unauthorized?" without the rest of the stack
// confounding the result.
//
// Cost: each call is a real API request. The prompt is one line
// and max_tokens is capped at 8, so per-model cost is fractions
// of a cent for cloud providers and free for local servers.

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/provider"
	anthpkg "github.com/guygrigsby/talon/internal/provider/anthropic"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/guygrigsby/talon/internal/secrets"
)

const (
	modelsTestPrompt    = "Reply with the single word: ok"
	modelsTestMaxTokens = 8
	modelsTestTimeout   = 30 * time.Second
)

// modelTestResult is one row in the report.
type modelTestResult struct {
	Provider string
	ID       string

	// Skipped marks the row as not exercised — auth missing, baseUrl
	// not configured, etc. Status carries the reason.
	Skipped bool

	// Outcome fields (only populated when not Skipped):
	TTFB         time.Duration // time from Stream open to first delta
	Total        time.Duration // open to channel close
	OutputTokens int           // from provider usage delta if reported
	Status       string        // "ok" or the error message
	OK           bool
}

func modelsTestCmd() *cobra.Command {
	var concurrent int
	c := &cobra.Command{
		Use:   "test",
		Short: "Sweep every configured model with a smoke probe (latency + error capture)",
		Long: `Fires "Reply with the single word: ok" at every model under
models.providers.<name>.models[] and reports per-model TTFB + total
latency. Each call is a real API request; the prompt is short and
max_tokens is capped at 8 so cost is minimal.

This bypasses the plugin subprocess and chat loop — it talks to each
provider directly using the in-tree openai/anthropic packages, so
the numbers attribute to provider/network only.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelsTest(cmd.OutOrStdout(), concurrent)
		},
	}
	c.Flags().IntVar(&concurrent, "concurrent", 4, "max parallel probes (cloud providers tolerate this fine; reduce for local servers)")
	return c
}

func runModelsTest(out io.Writer, concurrent int) error {
	paths := resolvePaths()
	merged, err := config.MergedBytes(paths)
	if err != nil {
		return fmt.Errorf("read merged config: %w", err)
	}

	if concurrent < 1 {
		concurrent = 1
	}

	// Resolve auth once per provider.
	providerAuth := resolveTestProviderAuth(merged)

	// Build the work list from models.providers.<name>.models[].
	type job struct {
		Provider string
		ID       string
	}
	var jobs []job
	gjson.GetBytes(merged, "models.providers").ForEach(func(name, prov gjson.Result) bool {
		pname := name.Str
		if pname == "" {
			return true
		}
		prov.Get("models").ForEach(func(_, m gjson.Result) bool {
			if id := m.Get("id").Str; id != "" {
				jobs = append(jobs, job{Provider: pname, ID: id})
			}
			return true
		})
		return true
	})
	if len(jobs) == 0 {
		fmt.Fprintln(out, "no models configured under models.providers.<name>.models[]")
		return nil
	}

	// Concurrency-bounded fan-out.
	results := make([]modelTestResult, len(jobs))
	sem := make(chan struct{}, concurrent)
	done := make(chan int, len(jobs))
	for i, j := range jobs {
		sem <- struct{}{}
		go func(idx int, jb job) {
			defer func() {
				<-sem
				done <- idx
			}()
			results[idx] = probeOneModel(jb.Provider, jb.ID, providerAuth)
		}(i, j)
	}
	for range jobs {
		<-done
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Provider != results[j].Provider {
			return results[i].Provider < results[j].Provider
		}
		return results[i].ID < results[j].ID
	})

	renderModelsTestReport(out, results)
	return nil
}

// providerAuthInfo captures everything needed to talk to one provider.
type providerAuthInfo struct {
	BaseURL string
	APIKey  string
	Err     error // non-nil when resolution failed; probe will skip with Err.Error()
}

// resolveTestProviderAuth mirrors openaicompat + anthropic plugins'
// resolution chains, in one process, so this command works whether
// keys are inline, env, profile, or op:// refs.
func resolveTestProviderAuth(merged []byte) map[string]providerAuthInfo {
	out := map[string]providerAuthInfo{}

	// openai-compat tenants.
	gjson.GetBytes(merged, "plugins.entries.openai-compat.config.providers").ForEach(func(name, prov gjson.Result) bool {
		pname := name.Str
		if pname == "" {
			return true
		}
		info := providerAuthInfo{BaseURL: prov.Get("baseUrl").Str}
		key := prov.Get("apiKey").Str
		if key == "" {
			envName := strings.ToUpper(strings.ReplaceAll(pname, "-", "_")) + "_API_KEY"
			if v := os.Getenv(envName); v != "" {
				key = v
			}
		}
		if key != "" && secrets.IsReference(key) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			resolved, err := secrets.ResolveOrLiteral(ctx, key)
			cancel()
			if err != nil {
				info.Err = fmt.Errorf("resolve %s apiKey: %w", pname, err)
				out[pname] = info
				return true
			}
			key = resolved
		}
		info.APIKey = key
		out[pname] = info
		return true
	})

	// Anthropic plugin entry.
	if k := gjson.GetBytes(merged, "plugins.entries.anthropic.config.apiKey").Str; k != "" {
		info := providerAuthInfo{BaseURL: anthpkg.DefaultBaseURL}
		if secrets.IsReference(k) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			resolved, err := secrets.ResolveOrLiteral(ctx, k)
			cancel()
			if err != nil {
				info.Err = fmt.Errorf("resolve anthropic apiKey: %w", err)
			} else {
				info.APIKey = resolved
			}
		} else {
			info.APIKey = k
		}
		out["anthropic"] = info
	}

	return out
}

// probeOneModel fires one chat request and times the response.
func probeOneModel(providerName, model string, auth map[string]providerAuthInfo) modelTestResult {
	r := modelTestResult{Provider: providerName, ID: model}

	info, ok := auth[providerName]
	if !ok {
		r.Skipped = true
		r.Status = "no auth configured (provider not in openai-compat tenants and not anthropic)"
		return r
	}
	if info.Err != nil {
		r.Skipped = true
		r.Status = info.Err.Error()
		return r
	}
	if info.APIKey == "" && !isLoopbackBase(info.BaseURL) {
		r.Skipped = true
		r.Status = "no API key resolved"
		return r
	}

	ctx, cancel := context.WithTimeout(context.Background(), modelsTestTimeout)
	defer cancel()

	prov, err := constructProbeProvider(providerName, info)
	if err != nil {
		r.Status = err.Error()
		return r
	}

	req := provider.Request{
		Model: provider.ModelID(providerName + "/" + model),
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: modelsTestPrompt},
		},
		Options: provider.Options{MaxOutputTokens: modelsTestMaxTokens},
	}

	start := time.Now()
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		r.Total = time.Since(start)
		r.Status = trimErr(err.Error())
		return r
	}

	var ttfb time.Duration
	for d := range ch {
		switch d.Kind {
		case provider.DeltaText, provider.DeltaReasoning:
			if ttfb == 0 {
				ttfb = time.Since(start)
			}
		case provider.DeltaUsage:
			if d.Usage != nil {
				r.OutputTokens = d.Usage.OutputTokens
			}
		case provider.DeltaError:
			if d.Err != nil {
				r.Status = trimErr(d.Err.Error())
				r.Total = time.Since(start)
				return r
			}
		}
	}
	r.Total = time.Since(start)
	r.TTFB = ttfb
	if r.Status == "" {
		r.OK = true
		r.Status = "ok"
	}
	return r
}

func constructProbeProvider(providerName string, info providerAuthInfo) (provider.Provider, error) {
	if providerName == "anthropic" {
		return anthpkg.New(anthpkg.Options{APIKey: info.APIKey, BaseURL: info.BaseURL}), nil
	}
	if info.BaseURL == "" {
		return nil, fmt.Errorf("no baseUrl configured for %s under plugins.entries.openai-compat.config.providers", providerName)
	}
	return openai.New(openai.Options{
		APIKey:      info.APIKey,
		BaseURL:     info.BaseURL,
		Name:        providerName,
		ProviderKey: providerName,
	}), nil
}

func renderModelsTestReport(out io.Writer, results []modelTestResult) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tMODEL\tTTFB\tTOTAL\tTOKENS\tSTATUS")
	fmt.Fprintln(tw, "--------\t-----\t----\t-----\t------\t------")
	var ok, fail, skip int
	var totalTTFB, totalDur time.Duration
	for _, r := range results {
		var ttfbStr, totalStr, toksStr string
		switch {
		case r.Skipped:
			ttfbStr, totalStr, toksStr = "-", "-", "-"
			skip++
		case r.OK:
			ttfbStr = r.TTFB.Truncate(time.Millisecond).String()
			totalStr = r.Total.Truncate(time.Millisecond).String()
			toksStr = fmt.Sprintf("%d", r.OutputTokens)
			ok++
			totalTTFB += r.TTFB
			totalDur += r.Total
		default:
			ttfbStr = "-"
			if r.Total > 0 {
				totalStr = r.Total.Truncate(time.Millisecond).String()
			} else {
				totalStr = "-"
			}
			toksStr = "-"
			fail++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Provider, r.ID, ttfbStr, totalStr, toksStr, r.Status)
	}
	tw.Flush()

	fmt.Fprintln(out)
	fmt.Fprintf(out, "summary: %d ok, %d fail, %d skip\n", ok, fail, skip)
	if ok > 0 {
		fmt.Fprintf(out, "ok-only means: avg ttfb=%s avg total=%s\n",
			(totalTTFB / time.Duration(ok)).Truncate(time.Millisecond),
			(totalDur / time.Duration(ok)).Truncate(time.Millisecond))
	}
}

func isLoopbackBase(u string) bool {
	low := strings.ToLower(u)
	return strings.Contains(low, "://localhost") ||
		strings.Contains(low, "://127.0.0.1") ||
		strings.Contains(low, "://[::1]") ||
		strings.Contains(low, "://0.0.0.0")
}

func trimErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	// Collapse newlines so the table row stays single-line.
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}
