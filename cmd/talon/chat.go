package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/gateway"
	"github.com/spf13/cobra"
)

func chatCmd() *cobra.Command {
	var sessionKey string
	var agentID string
	var timeoutSec int
	var rawEvents bool

	c := &cobra.Command{
		Use:   "chat [message...]",
		Short: "Send a message and stream the reply",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("provide a message (or use --help)")
			}
			message := strings.Join(args, " ")

			if sessionKey == "" {
				if agentID == "" {
					agentID = "main"
				}
				sessionKey = fmt.Sprintf("agent:%s:%s", agentID, agentID)
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
			defer cancel()

			cfg, err := config.Load(resolvePaths())
			if err != nil {
				return err
			}
			cli := gateway.NewClient(cfg.GatewayURL(), cfg.Gateway.Auth.Token)

			done := make(chan struct{})
			var streamErr error
			var sawAny bool
			var printed string

			emitNew := func(text string) {
				if !strings.HasPrefix(text, printed) {
					// Non-cumulative delta or token replacement — just print as-is.
					fmt.Print(text)
					printed = text
					return
				}
				fmt.Print(text[len(printed):])
				printed = text
			}

			cli.OnEvent = func(event string, payload json.RawMessage) {
				if event != "chat" {
					return
				}
				var ev struct {
					RunID        string          `json:"runId"`
					SessionKey   string          `json:"sessionKey"`
					Seq          int             `json:"seq"`
					State        string          `json:"state"`
					Message      json.RawMessage `json:"message"`
					ErrorMessage string          `json:"errorMessage"`
					ErrorKind    string          `json:"errorKind"`
				}
				if err := json.Unmarshal(payload, &ev); err != nil {
					return
				}
				if ev.SessionKey != sessionKey {
					return
				}
				sawAny = true
				if rawEvents {
					fmt.Println(string(payload))
				} else if text := extractAssistantText(ev.Message); text != "" {
					emitNew(text)
				}
				switch ev.State {
				case "final":
					if !rawEvents {
						fmt.Println()
					}
					close(done)
				case "aborted":
					streamErr = fmt.Errorf("aborted")
					close(done)
				case "error":
					if ev.ErrorMessage != "" {
						streamErr = fmt.Errorf("%s: %s", ev.ErrorKind, ev.ErrorMessage)
					} else {
						streamErr = fmt.Errorf("error: %s", ev.ErrorKind)
					}
					close(done)
				}
			}

			if err := cli.Connect(ctx); err != nil {
				return err
			}
			defer cli.Close()

			idem, err := newIdempotencyKey()
			if err != nil {
				return err
			}
			params := map[string]any{
				"sessionKey":     sessionKey,
				"message":        message,
				"idempotencyKey": idem,
			}
			if _, err := cli.Request(ctx, "chat.send", params); err != nil {
				return fmt.Errorf("chat.send: %w", err)
			}

			select {
			case <-done:
				if !sawAny {
					return fmt.Errorf("no chat events received")
				}
				return streamErr
			case <-ctx.Done():
				return fmt.Errorf("timeout after %ds (no final received)", timeoutSec)
			}
		},
	}
	c.Flags().StringVar(&sessionKey, "session", "", "session key (default: agent:<agent>:<agent>)")
	c.Flags().StringVar(&agentID, "agent", "", "agent id to chat with (default: main)")
	c.Flags().IntVar(&timeoutSec, "timeout", 300, "overall timeout in seconds")
	c.Flags().BoolVar(&rawEvents, "raw", false, "print raw event JSON instead of streaming text")
	return c
}

func newIdempotencyKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "talon-" + hex.EncodeToString(b), nil
}

// extractAssistantText pulls visible text out of an assistant-message payload.
// The gateway message shape varies; we try a few common shapes and fall back.
func extractAssistantText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Shape A: {"phase":"assistant","content":[{"type":"text","text":"..."}],...}
	var withContent struct {
		Phase   string `json:"phase"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &withContent); err == nil {
		if withContent.Role != "" && withContent.Role != "assistant" {
			return ""
		}
		var b strings.Builder
		for _, c := range withContent.Content {
			if c.Type == "text" || c.Type == "" {
				b.WriteString(c.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	// Shape B: {"text":"..."}
	var withText struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &withText); err == nil && withText.Text != "" {
		return withText.Text
	}
	return ""
}

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

// silence unused imports during dev
var _ = os.Args
