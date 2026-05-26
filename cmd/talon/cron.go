package main

// `talon cron` thin wrapper over the gateway's cron.* RPC surface
// (cron.list, cron.add, cron.remove, cron.run, cron.status, cron.runs)
// shipped in talon-8z0. Out-of-scope items such as per-job timezones,
// jitter, and isolated sessions are noted as TODOs for follow-up work.

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// cronJob mirrors the gateway-side cron.Job shape just enough to
// render. Times are unix-millis on the wire.
type cronJob struct {
	ID          string        `json:"id"`
	Expression  string        `json:"expression"`
	Action      cronJobAction `json:"action"`
	Enabled     bool          `json:"enabled"`
	NextRunMs   int64         `json:"nextRunMs,omitempty"`
	LastRunMs   int64         `json:"lastRunMs,omitempty"`
	LastStatus  string        `json:"lastStatus,omitempty"`
	LastErr     string        `json:"lastErr,omitempty"`
	CreatedAtMs int64         `json:"createdAtMs"`
}

type cronJobAction struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type cronRunRecord struct {
	RunID       string `json:"runId"`
	JobID       string `json:"jobId"`
	Method      string `json:"method"`
	StartedAtMs int64  `json:"startedAtMs"`
	DurationMs  int64  `json:"durationMs"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Manual      bool   `json:"manual,omitempty"`
}

func cronCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cron",
		Short: "Manage scheduled jobs in the talon gateway",
		RunE:  func(_ *cobra.Command, _ []string) error { return cronListRun(false) },
	}
	c.AddCommand(cronListCmd())
	c.AddCommand(cronAddCmd())
	cronRemove := cronRemoveCmd()
	cronRemove.Aliases = []string{"rm"}
	c.AddCommand(cronRemove)
	c.AddCommand(cronRunCmd())
	c.AddCommand(cronStatusCmd())
	c.AddCommand(cronShowCmd())
	c.AddCommand(cronEnableCmd())
	c.AddCommand(cronDisableCmd())
	c.AddCommand(cronRunsCmd())
	return c
}

func cronListCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List scheduled jobs (enabled only by default)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return cronListRun(all)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "include disabled jobs")
	return c
}

func cronListRun(all bool) error {
	var body any
	if all {
		body = map[string]any{"all": true}
	}
	payload, err := runRPC("cron.list", body)
	if err != nil {
		return err
	}
	if flagJSON {
		emit(payload)
		return nil
	}
	var r struct {
		Jobs []cronJob `json:"jobs"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return fmt.Errorf("decode cron.list: %w", err)
	}
	renderJobs(r.Jobs)
	return nil
}

func cronAddCmd() *cobra.Command {
	var (
		expression string
		method     string
		params     string
		paramsFile string
		disabled   bool
	)
	c := &cobra.Command{
		Use:   "add <id>",
		Short: "Create or replace a scheduled job",
		Long: `Schedule a registry RPC to run on a cron schedule. The action.method
must be a session-agnostic registry RPC (chat.send is NOT today;
custom handlers can be written that don't require a Session).

Examples:
  talon cron add daily-summary --expr '0 9 * * *' --method system.echo --params '{"text":"morning"}'
  talon cron add hourly --expr '@hourly' --method health
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if strings.TrimSpace(expression) == "" {
				return fmt.Errorf("--expr is required")
			}
			if strings.TrimSpace(method) == "" {
				return fmt.Errorf("--method is required")
			}
			body := map[string]any{
				"id":         id,
				"expression": expression,
				"method":     method,
				"enabled":    !disabled,
			}
			rawParams, err := readParams(params, paramsFile)
			if err != nil {
				return err
			}
			if rawParams != nil {
				body["params"] = json.RawMessage(rawParams)
			}
			payload, err := runRPC("cron.add", body)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Job cronJob `json:"job"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode cron.add: %w", err)
			}
			fmt.Printf("✓ added %s (next run %s)\n", r.Job.ID, formatMs(r.Job.NextRunMs))
			return nil
		},
	}
	c.Flags().StringVar(&expression, "expr", "", "cron expression (5-field, 6-field, or @hourly/@daily/etc.)")
	c.Flags().StringVar(&method, "method", "", "registry RPC method to invoke when the job fires")
	c.Flags().StringVar(&params, "params", "", "inline JSON params for the action (use --params-file for paths)")
	c.Flags().StringVar(&paramsFile, "params-file", "", "read action params JSON from a file")
	c.Flags().BoolVar(&disabled, "disabled", false, "create the job in disabled state")
	return c
}

func cronRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Delete a scheduled job",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			payload, err := runRPC("cron.remove", map[string]any{"id": args[0]})
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Removed bool `json:"removed"`
			}
			_ = json.Unmarshal(payload, &r)
			if r.Removed {
				fmt.Printf("✓ removed %s\n", args[0])
			} else {
				fmt.Printf("(not found) %s\n", args[0])
			}
			return nil
		},
	}
}

func cronRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <id>",
		Short: "Fire a scheduled job immediately, ignoring its schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			payload, err := runRPC("cron.run", map[string]any{"id": args[0]})
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Run cronRunRecord `json:"run"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode cron.run: %w", err)
			}
			status := "✓ ok"
			if !r.Run.OK {
				status = "✗ error"
			}
			fmt.Printf("%s  %s  %s  %s\n", status, r.Run.RunID, r.Run.Method, formatDuration(r.Run.DurationMs))
			if r.Run.Error != "" {
				fmt.Printf("        %s\n", r.Run.Error)
			}
			return nil
		},
	}
}

func cronStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cron scheduler status (running, job counts, next fire)",
		RunE: func(_ *cobra.Command, _ []string) error {
			payload, err := runRPC("cron.status", nil)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var st struct {
				Running      bool  `json:"running"`
				JobCount     int   `json:"jobCount"`
				EnabledCount int   `json:"enabledCount"`
				NextRunMs    int64 `json:"nextRunMs"`
			}
			if err := json.Unmarshal(payload, &st); err != nil {
				return fmt.Errorf("decode cron.status: %w", err)
			}
			state := "stopped"
			if st.Running {
				state = "running"
			}
			fmt.Printf("scheduler: %s  jobs: %d  enabled: %d  next: %s\n",
				state, st.JobCount, st.EnabledCount, formatMs(st.NextRunMs))
			return nil
		},
	}
}

func cronShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a scheduled job by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			payload, err := runRPC("cron.show", map[string]any{"id": args[0]})
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Job cronJob `json:"job"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode cron.show: %w", err)
			}
			renderJobs([]cronJob{r.Job})
			return nil
		},
	}
}

func cronEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a scheduled job",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			payload, err := runRPC("cron.enable", map[string]any{"id": args[0]})
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			fmt.Printf("✓ enabled %s\n", args[0])
			_ = payload
			return nil
		},
	}
}

func cronDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a scheduled job",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			payload, err := runRPC("cron.disable", map[string]any{"id": args[0]})
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			fmt.Printf("✓ disabled %s\n", args[0])
			_ = payload
			return nil
		},
	}
}

func cronRunsCmd() *cobra.Command {
	var (
		jobID string
		limit int
	)
	c := &cobra.Command{
		Use:   "runs",
		Short: "List recent scheduled-job runs (newest first)",
		RunE: func(_ *cobra.Command, _ []string) error {
			body := map[string]any{}
			if jobID != "" {
				body["id"] = jobID
			}
			if limit > 0 {
				body["limit"] = limit
			}
			payload, err := runRPC("cron.runs", body)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Runs []cronRunRecord `json:"runs"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode cron.runs: %w", err)
			}
			renderRuns(r.Runs)
			return nil
		},
	}
	c.Flags().StringVar(&jobID, "id", "", "filter to a single job id")
	c.Flags().IntVar(&limit, "limit", 0, "max runs to return (server caps at 1000)")
	return c
}

// readParams resolves inline JSON or a JSON file into raw bytes,
// validating shape so the gateway sees a clean map. Returns nil bytes
// when neither flag is set.
func readParams(inline, file string) ([]byte, error) {
	if inline != "" && file != "" {
		return nil, fmt.Errorf("--params and --params-file are mutually exclusive")
	}
	if inline != "" {
		var v any
		if err := json.Unmarshal([]byte(inline), &v); err != nil {
			return nil, fmt.Errorf("--params is not valid JSON: %w", err)
		}
		return []byte(inline), nil
	}
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("--params-file: %w", err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("--params-file: %w", err)
		}
		return raw, nil
	}
	return nil, nil
}

// renderJobs prints a tab-aligned table sorted by id (the server
// already sorts; we re-render verbatim). Columns are id, schedule,
// method, and status.
func renderJobs(jobs []cronJob) {
	if len(jobs) == 0 {
		fmt.Println("(no jobs)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "ID\tSCHEDULE\tMETHOD\tSTATE\tNEXT RUN\tLAST RUN")
	for _, j := range jobs {
		state := "enabled"
		if !j.Enabled {
			state = "disabled"
		}
		if j.LastStatus == "error" {
			state += " (last err)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			j.ID,
			j.Expression,
			j.Action.Method,
			state,
			formatMs(j.NextRunMs),
			formatMs(j.LastRunMs),
		)
	}
}

func renderRuns(runs []cronRunRecord) {
	if len(runs) == 0 {
		fmt.Println("(no runs)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "WHEN\tJOB\tMETHOD\tSTATUS\tDURATION\tNOTE")
	for _, r := range runs {
		status := "ok"
		if !r.OK {
			status = "ERROR"
		}
		note := ""
		if r.Manual {
			note = "manual"
		}
		if r.Error != "" {
			if note != "" {
				note += "; "
			}
			note += r.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			formatMs(r.StartedAtMs),
			r.JobID,
			r.Method,
			status,
			formatDuration(r.DurationMs),
			note,
		)
	}
}

// formatMs renders a unix-millis timestamp in local time. Empty
// timestamps (0 / unset) render as "—" so the table stays
// consistent-width without a wall of zeros.
func formatMs(ms int64) string {
	if ms == 0 {
		return "—"
	}
	t := time.UnixMilli(ms).Local()
	return t.Format("2006-01-02 15:04:05")
}

func formatDuration(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	return time.Duration(ms * int64(time.Millisecond)).Truncate(time.Millisecond).String()
}
