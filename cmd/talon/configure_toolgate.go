package main

// `talon configure toolgate` sets the pinion tool-use gate enforcement mode
// (ADR 0017). The gate classifies every agent tool call and, in enforce mode,
// refuses calls that exceed the agent's capability grant or complete a
// dangerous flow (e.g. read-a-secret then send-to-network). This wizard is the
// guided way to pick the mode; per-agent grant widening stays in config under
// agents.list[].toolgate.allow.

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/guygrigsby/talon/internal/config"
)

func configureToolgate(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	paths := resolvePaths()

	current := "enforce"
	if merged, err := config.MergedBytes(paths); err == nil {
		if v := gjson.GetBytes(merged, "toolgate.mode").Str; v != "" {
			current = v
		}
	}

	fmt.Fprintln(out, "Tool-use safety gate (ADR 0017)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Every agent tool call is classified by its capabilities (filesystem,")
	fmt.Fprintln(out, "exec, network, secrets) and cross-call data flows. The mode decides")
	fmt.Fprintln(out, "what happens on a risky call:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  1) enforce  refuse calls outside the grant or completing an exfil flow (default)")
	fmt.Fprintln(out, "  2) audit    record every verdict but never block")
	fmt.Fprintln(out, "  3) off      no gating")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Current mode: %s\n", current)
	fmt.Fprintf(out, "Choose [1-3] (blank keeps %s): ", current)

	line, _ := reader.ReadString('\n')
	choice := strings.TrimSpace(line)

	mode := current
	switch choice {
	case "":
		// keep current
	case "1", "enforce":
		mode = "enforce"
	case "2", "audit":
		mode = "audit"
	case "3", "off":
		mode = "off"
	default:
		return fmt.Errorf("invalid choice %q (want 1, 2, or 3)", choice)
	}

	if _, err := config.Set(paths, []string{"toolgate", "mode"}, mode, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
		return fmt.Errorf("write toolgate.mode: %w", err)
	}
	fmt.Fprintf(out, "\nToolgate mode set to %q.\n", mode)
	if mode == "enforce" {
		fmt.Fprintln(out, "Note: bash (exec) and network access are denied by default. To re-enable")
		fmt.Fprintln(out, "for an agent, add capabilities under agents.list[].toolgate.allow, e.g.")
		fmt.Fprintln(out, `  toolgate = { allow = ["exec"] }`)
	}
	fmt.Fprintln(out, "Inspect the effective gate with: talon toolgate")
	return nil
}

func configureToolgateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "toolgate",
		Short: "Set the tool-use safety gate mode (enforce/audit/off)",
		Long: `Set the pinion tool-use gate enforcement mode (ADR 0017). The gate
classifies every agent tool call and, in enforce mode, refuses calls that
exceed the agent's capability grant or complete a dangerous data flow.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return configureToolgate(cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
