package main

// `talon audit show` reads the agent-action audit trail
// (~/.talon/logs/agent-audit.jsonl plus rotated .1, .2, …) and prints a
// filtered, time-ordered view. The trail is the durable, redacted record
// written by the gateway (ADR 0011); this command is how an operator traces
// a session/run after a failure.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/guygrigsby/talon/internal/audit"
)

func auditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "audit",
		Short: "Inspect the agent-action audit trail",
		Long: `Read the durable, redacted agent-action audit log written by the
gateway (~/.talon/logs/agent-audit.jsonl). Use this to trace what an agent
did in a session or run after a failure or restart.`,
	}
	c.AddCommand(auditShowCmd())
	return c
}

func auditShowCmd() *cobra.Command {
	var (
		session string
		run     string
		since   time.Duration
		asJSON  bool
	)
	c := &cobra.Command{
		Use:   "show",
		Short: "Print the agent-action trail, optionally filtered",
		Long: `Print the agent-action audit trail in time order.

  talon audit show                          # everything
  talon audit show --session agent:main:web # one session
  talon audit show --run run-123            # one run
  talon audit show --since 1h               # only the last hour
  talon audit show --json                   # raw JSONL passthrough`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths := resolvePaths()
			events, err := loadAuditEvents(paths.Talon.AgentAuditLogPath())
			if err != nil {
				return err
			}
			var cutoff time.Time
			if since > 0 {
				cutoff = time.Now().Add(-since)
			}
			filtered := filterAuditEvents(events, session, run, cutoff)
			return renderAuditEvents(cmd.OutOrStdout(), filtered, asJSON)
		},
	}
	c.Flags().StringVar(&session, "session", "", "filter to one session key")
	c.Flags().StringVar(&run, "run", "", "filter to one run id")
	c.Flags().DurationVar(&since, "since", 0, "only events newer than this duration (e.g. 1h, 30m)")
	c.Flags().BoolVar(&asJSON, "json", false, "emit raw JSONL instead of the formatted view")
	return c
}

// auditFilePaths returns the live trail plus any rotated siblings (.1, .2, …)
// that exist, so a `show` after rotation still sees the older records.
func auditFilePaths(base string) []string {
	out := []string{base}
	for n := 1; ; n++ {
		p := base + "." + strconv.Itoa(n)
		if _, err := os.Stat(p); err != nil {
			break
		}
		out = append(out, p)
	}
	return out
}

// loadAuditEvents parses every audit record across the live + rotated files.
// A missing live file is not an error (the trail just hasn't been written
// yet). Malformed lines are skipped so one bad record doesn't blank the view.
func loadAuditEvents(base string) ([]audit.Event, error) {
	var events []audit.Event
	for _, path := range auditFilePaths(base) {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var e audit.Event
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			events = append(events, e)
		}
		_ = f.Close()
	}
	return events, nil
}

// filterAuditEvents keeps events matching the session/run filters (empty =
// match all) that are at or after cutoff (zero = no time floor), then sorts
// by timestamp, breaking ties on Seq so a run's actions stay ordered.
func filterAuditEvents(events []audit.Event, session, run string, cutoff time.Time) []audit.Event {
	out := make([]audit.Event, 0, len(events))
	for _, e := range events {
		if session != "" && e.Session != session {
			continue
		}
		if run != "" && e.Run != run {
			continue
		}
		if !cutoff.IsZero() && e.Ts.Before(cutoff) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ts.Equal(out[j].Ts) {
			return out[i].Seq < out[j].Seq
		}
		return out[i].Ts.Before(out[j].Ts)
	})
	return out
}

func renderAuditEvents(w io.Writer, events []audit.Event, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range events {
		line := fmt.Sprintf("%s  %-12s", e.Ts.Format(time.RFC3339), e.Kind)
		switch e.Kind {
		case audit.KindToolCall, audit.KindToolResult:
			line += "  " + e.Tool
			if e.IsError {
				line += " [error]"
			}
		case audit.KindToolGate:
			line += "  " + e.Tool + " " + e.Verdict
		case audit.KindTurnStart:
			if e.Model != "" {
				line += "  " + e.Model
			}
		case audit.KindError:
			line += "  " + e.ErrKind
		}
		fmt.Fprintf(w, "%s  %s/%s#%d\n", line, e.Session, e.Run, e.Seq)
	}
	return nil
}
