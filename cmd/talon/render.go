package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// modelEntry mirrors the per-model shape returned by the models.list RPC.
// Only the fields used by the renderer are declared; unknown fields are
// preserved by the json package's defaults (ignored on decode).
type modelEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	ContextWindow int      `json:"contextWindow"`
	Input         []string `json:"input"`
	Reasoning     bool     `json:"reasoning"`
	Alias         string   `json:"alias,omitempty"`
}

type modelsEnvelope struct {
	Models []modelEntry `json:"models"`
}

// renderModels prints a tab-aligned summary of models.list. Columns:
// ID, MODALITIES, CTX, REASONING, ALIAS, NAME. Rows are sorted by ID for
// stable output.
func renderModels(w io.Writer, payload json.RawMessage) error {
	var env modelsEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("decode models payload: %w", err)
	}
	rows := append([]modelEntry(nil), env.Models...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMODALITIES\tCTX\tREASONING\tALIAS\tNAME")
	for _, m := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			m.ID,
			strings.Join(m.Input, ","),
			formatCtx(m.ContextWindow),
			boolGlyph(m.Reasoning),
			emptyDash(m.Alias),
			m.Name,
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no models)")
	}
	return nil
}

type agentEntry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Model     agentModel `json:"model"`
}

type agentModel struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

type agentsEnvelope struct {
	Agents    []agentEntry `json:"agents"`
	DefaultID string       `json:"defaultId"`
}

// renderAgents prints a tab-aligned summary of agents.list. Columns:
// ID, MODEL, WORKSPACE, FALLBACKS, NAME. The default agent is marked with
// "(default)" after its ID.
func renderAgents(w io.Writer, payload json.RawMessage) error {
	var env agentsEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("decode agents payload: %w", err)
	}
	rows := append([]agentEntry(nil), env.Agents...)
	sort.Slice(rows, func(i, j int) bool {
		// default agent first, then alphabetical.
		if rows[i].ID == env.DefaultID {
			return true
		}
		if rows[j].ID == env.DefaultID {
			return false
		}
		return rows[i].ID < rows[j].ID
	})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMODEL\tWORKSPACE\tFALLBACKS\tNAME")
	for _, a := range rows {
		idCell := a.ID
		if a.ID == env.DefaultID {
			idCell += " (default)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			idCell,
			emptyDash(a.Model.Primary),
			emptyDash(homeShorten(a.Workspace)),
			len(a.Model.Fallbacks),
			emptyDash(a.Name),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no agents)")
	}
	return nil
}

func boolGlyph(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatCtx renders a context-window int as a compact string with K/M
// suffixes. 200000 → 200K, 1000000 → 1M, 131072 → 128K.
func formatCtx(n int) string {
	switch {
	case n <= 0:
		return "-"
	case n%1_000_000 == 0:
		return fmt.Sprintf("%dM", n/1_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n%1000 == 0:
		return fmt.Sprintf("%dK", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%dK", (n+512)/1024) // round to nearest KiB for ~131072 → 128K
	default:
		return fmt.Sprintf("%d", n)
	}
}

// homeShorten replaces a leading $HOME with "~" for compact display.
// Pure best-effort; returns input unchanged if the lookup or prefix check
// fails.
func homeShorten(p string) string {
	if p == "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}
