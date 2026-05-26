// Package main implements `talon agents bindings`, a read-only listing of
// channel-to-agent routing entries from the merged config.
//
// Talon today writes `channels.<id>.agentId` for configured channels.
// Additional match shapes can extend this command without changing its
// top-level shape.

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
)

// binding is one row in the bindings list.
type binding struct {
	Channel     string `json:"channel"`
	AgentID     string `json:"agentId"`
	Description string `json:"description"`
}

func agentsBindingsCmd() *cobra.Command {
	var filterAgent string
	c := &cobra.Command{
		Use:   "bindings",
		Short: "List channel→agent routing bindings",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := resolvePaths()
			merged, err := config.MergedBytes(paths)
			if err != nil {
				return fmt.Errorf("read merged config: %w", err)
			}

			bindings := scanChannelBindings(merged)
			filterAgent = strings.TrimSpace(filterAgent)
			if filterAgent != "" {
				kept := bindings[:0]
				for _, b := range bindings {
					if b.AgentID == filterAgent {
						kept = append(kept, b)
					}
				}
				bindings = kept
			}

			if flagJSON {
				out, _ := json.MarshalIndent(bindings, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			if len(bindings) == 0 {
				if filterAgent != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "No routing bindings for agent %q.\n", filterAgent)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "No routing bindings.")
				}
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "CHANNEL\tAGENT\tDESCRIPTION")
			for _, b := range bindings {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", b.Channel, b.AgentID, b.Description)
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&filterAgent, "agent", "", "filter by agent id")
	return c
}

// scanChannelBindings walks channels.<id>.agentId entries from the
// merged config. Stable order so JSON / table output is reproducible.
func scanChannelBindings(merged []byte) []binding {
	out := []binding{}
	channels := gjson.GetBytes(merged, "channels")
	if !channels.Exists() || !channels.IsObject() {
		return out
	}
	channels.ForEach(func(key, value gjson.Result) bool {
		chID := key.Str
		if chID == "" || !value.IsObject() {
			return true
		}
		agentID := strings.TrimSpace(value.Get("agentId").String())
		if agentID == "" {
			return true
		}
		desc := describeChannelBinding(chID, value)
		out = append(out, binding{
			Channel:     chID,
			AgentID:     agentID,
			Description: desc,
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}

// describeChannelBinding formats a one-line summary of the channel's
// configuration relevant to routing — dmPolicy + first allowFrom
// entry for telegram, etc. Helps the user understand who's actually
// being routed without grepping the config.
func describeChannelBinding(chID string, entry gjson.Result) string {
	parts := []string{}
	if v := entry.Get("dmPolicy").Str; v != "" {
		parts = append(parts, "dmPolicy="+v)
	}
	if v := entry.Get("allowFrom"); v.IsArray() {
		ids := []string{}
		v.ForEach(func(_, val gjson.Result) bool {
			if s := val.String(); s != "" {
				ids = append(ids, s)
			}
			return true
		})
		if len(ids) > 0 {
			parts = append(parts, "allowFrom=["+strings.Join(ids, ",")+"]")
		}
	}
	if v := entry.Get("botToken"); v.Exists() && v.Str != "" {
		parts = append(parts, "token=set")
	}
	return strings.Join(parts, " ")
}
