package main

// `talon toolgate <agent>` shows the effective pinion tool-use gate for an
// agent (ADR 0017): the enforcement mode, the capability grant the agent runs
// under, and the most recent gate verdicts from the audit trail. This is the
// operator's window into why a tool call was allowed or refused.

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/guygrigsby/talon/internal/audit"
	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/toolgate"
)

func toolgateCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "toolgate [agent]",
		Short: "Show the tool-use gate (mode, grant, recent verdicts) for an agent",
		Long: `Show the pinion tool-use gate for an agent (ADR 0017): its enforcement
mode, the capability grant it runs under, and recent gate verdicts.

  talon toolgate              # the main agent
  talon toolgate coding       # a named agent
  talon toolgate --limit 50   # show more recent verdicts

Change the mode with: talon config set toolgate.mode <off|audit|enforce>
Widen an agent's grant with agents.list[].toolgate.allow (e.g. ["exec"]).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := "main"
			if len(args) == 1 && args[0] != "" {
				agentID = args[0]
			}
			paths := resolvePaths()
			merged, err := config.MergedBytes(paths)
			if err != nil {
				return err
			}
			events, err := loadAuditEvents(paths.Talon.AgentAuditLogPath())
			if err != nil {
				return err
			}
			if limit > 0 {
				events = recentToolgate(events, agentID, limit)
			}
			return renderToolgate(cmd.OutOrStdout(), merged, agentID, events)
		},
	}
	c.Flags().IntVar(&limit, "limit", 20, "max recent gate verdicts to show")
	return c
}

// toolgateWorkspace resolves the agent workspace the same way chatdriver does:
// per-agent agents.list[].workspace, else agents.defaults.workspace.
func toolgateWorkspace(merged []byte, agentID string) string {
	if v := gjson.GetBytes(merged, fmt.Sprintf("agents.list.#(id==%q).workspace", agentID)).Str; v != "" {
		return v
	}
	return gjson.GetBytes(merged, "agents.defaults.workspace").Str
}

// toolgateMode resolves the gate mode: per-agent override, else global, else
// enforce.
func toolgateMode(merged []byte, agentID string) toolgate.Mode {
	if v := gjson.GetBytes(merged, fmt.Sprintf("agents.list.#(id==%q).toolgate.mode", agentID)).Str; v != "" {
		return toolgate.ParseMode(v)
	}
	return toolgate.ParseMode(gjson.GetBytes(merged, "toolgate.mode").Str)
}

// toolgateAllow resolves the per-agent extra grant kinds, else global defaults.
func toolgateAllow(merged []byte, agentID string) []string {
	res := gjson.GetBytes(merged, fmt.Sprintf("agents.list.#(id==%q).toolgate.allow", agentID))
	if !res.Exists() {
		res = gjson.GetBytes(merged, "toolgate.defaults.allow")
	}
	out := make([]string, 0, len(res.Array()))
	for _, v := range res.Array() {
		out = append(out, v.String())
	}
	return out
}

// recentToolgate keeps the most recent up-to-limit tool_gate events for the
// agent, newest last (time order).
func recentToolgate(events []audit.Event, agentID string, limit int) []audit.Event {
	var gate []audit.Event
	for _, e := range events {
		if e.Kind == audit.KindToolGate && (e.Agent == "" || e.Agent == agentID) {
			gate = append(gate, e)
		}
	}
	sort.SliceStable(gate, func(i, j int) bool { return gate[i].Ts.Before(gate[j].Ts) })
	if len(gate) > limit {
		gate = gate[len(gate)-limit:]
	}
	return gate
}

// renderToolgate writes the effective gate view for agentID: mode, workspace,
// the effective capability grant, and the recent gate verdicts in events.
func renderToolgate(w io.Writer, merged []byte, agentID string, events []audit.Event) error {
	mode := toolgateMode(merged, agentID)
	workspace := toolgateWorkspace(merged, agentID)
	grant := toolgate.GrantWith(workspace, toolgateAllow(merged, agentID))

	fmt.Fprintf(w, "agent:     %s\n", agentID)
	fmt.Fprintf(w, "mode:      %s\n", mode)
	if workspace == "" {
		fmt.Fprintf(w, "workspace: (none)\n")
	} else {
		fmt.Fprintf(w, "workspace: %s\n", workspace)
	}

	fmt.Fprintf(w, "grant (allowed capabilities):\n")
	for _, e := range grant.Allowed {
		scope := e.Scope.Pattern
		if scope == "" {
			scope = "(unscoped)"
		}
		fmt.Fprintf(w, "  - %s %s\n", e.Kind, scope)
	}

	// Only gate verdicts for this agent, in time order.
	gate := recentToolgate(events, agentID, len(events)+1)
	fmt.Fprintf(w, "recent verdicts (%d):\n", len(gate))
	if len(gate) == 0 {
		fmt.Fprintf(w, "  (none recorded yet)\n")
		return nil
	}
	for _, e := range gate {
		ts := ""
		if !e.Ts.IsZero() {
			ts = e.Ts.Format(time.RFC3339) + "  "
		}
		line := fmt.Sprintf("  %s%-14s %s", ts, e.Verdict, e.Tool)
		if e.Text != "" {
			line += "  (" + e.Text + ")"
		}
		fmt.Fprintln(w, line)
	}
	return nil
}
