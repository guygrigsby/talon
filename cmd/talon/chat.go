package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// chatCmd (streaming one-shot send) was deleted in the WS strip.
// The interactive chat experience lives in the web FE; a TUI
// replacement is tracked as talon-dcj. For now only chat-history
// remains here.

func chatHistoryCmd() *cobra.Command {
	var sessionKey string
	var agentID string
	var limit int
	c := &cobra.Command{
		Use:   "history",
		Short: "Print chat history for a session",
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionKey == "" {
				if agentID == "" {
					agentID = "main"
				}
				sessionKey = fmt.Sprintf("agent:%s:%s", agentID, agentID)
			}
			payload, err := runRPC("chat.history", map[string]any{
				"sessionKey": sessionKey,
				"limit":      limit,
			})
			if err != nil {
				return err
			}
			emit(payload)
			return nil
		},
	}
	c.Flags().StringVar(&sessionKey, "session", "", "session key")
	c.Flags().StringVar(&agentID, "agent", "", "agent id (default: main)")
	c.Flags().IntVar(&limit, "limit", 50, "max messages to return")
	return c
}
