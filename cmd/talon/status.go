package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	var (
		jsonFlag  bool
		all       bool
		usage     bool
		deep      bool
		timeoutMs int
		verbose   bool
		debug     bool
	)
	c := &cobra.Command{
		Use:   "status",
		Short: "Show channel health and recent session recipients",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = jsonFlag
			_ = verbose
			_ = debug
			if all {
				fmt.Fprintln(os.Stderr, "talon: --all not yet wired (channels.status only)")
			}
			if usage {
				fmt.Fprintln(os.Stderr, "talon: --usage not yet wired (channels.status only)")
			}
			payload, err := runRPC("channels.status", map[string]any{
				"probe":     deep,
				"timeoutMs": timeoutMs,
			})
			if err != nil {
				return err
			}
			emit(payload)
			return nil
		},
	}
	c.Flags().BoolVar(&jsonFlag, "json", false, "Output JSON instead of text")
	c.Flags().BoolVar(&all, "all", false, "Full diagnosis (read-only, pasteable)")
	c.Flags().BoolVar(&usage, "usage", false, "Show model provider usage/quota snapshots")
	c.Flags().BoolVar(&deep, "deep", false, "Probe channels (WhatsApp Web + Telegram + Discord + Slack + Signal)")
	c.Flags().IntVar(&timeoutMs, "timeout", 10000, "Probe timeout in milliseconds")
	c.Flags().BoolVar(&verbose, "verbose", false, "Verbose logging")
	c.Flags().BoolVar(&debug, "debug", false, "Alias for --verbose")
	return c
}
