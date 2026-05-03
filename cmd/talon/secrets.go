// Package main — `talon secrets` command surface.
//
// `talon secrets ls`       — audit which sensitive paths in your
//                            merged config are LITERAL plaintext
//                            vs. already moved to a reference
//                            (op://, keychain://). The output is
//                            the punch list for migration.
//
// Future subcommands (this commit ships ls only, migrate +
// keychain-bootstrap follow):
//   `talon secrets migrate <path>`        — move an on-disk secret
//                                           into 1Password and
//                                           replace it with op://...
//   `talon secrets keychain-bootstrap`    — store the 1P service-
//                                           account token in the
//                                           macOS keychain so the
//                                           op CLI can authenticate
//                                           non-interactively.

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
)

func secretsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "secrets",
		Short: "Inspect and migrate on-disk secrets",
	}
	c.AddCommand(secretsLsCmd())
	return c
}

// secretsAuditEntry is one row in the audit table: the dotted
// config path + a classification of the value (literal/ref/empty)
// + a short reason ("plaintext token", "op:// reference", etc.).
type secretsAuditEntry struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`  // "literal" | "ref" | "empty"
	Value string `json:"value"` // ref string OR "[REDACTED]" for literals
	Note  string `json:"note,omitempty"`
}

func secretsLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List sensitive paths in the merged config (literal vs reference)",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := resolvePaths()
			merged, err := config.MergedBytes(paths)
			if err != nil {
				return fmt.Errorf("read merged config: %w", err)
			}
			entries := auditSecrets(merged)
			if flagJSON {
				out, _ := json.MarshalIndent(entries, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No sensitive paths found in merged config.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PATH\tKIND\tVALUE/REF")
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Path, e.Kind, e.Value)
			}
			tw.Flush()
			anyLiteral := false
			for _, e := range entries {
				if e.Kind == "literal" {
					anyLiteral = true
					break
				}
			}
			if anyLiteral {
				fmt.Fprintln(cmd.OutOrStdout(), "")
				fmt.Fprintln(cmd.OutOrStdout(), "Plaintext secrets detected. To migrate:")
				fmt.Fprintln(cmd.OutOrStdout(), "  1) talon secrets keychain-bootstrap   (one time, sets up 1P service-account token in keychain)")
				fmt.Fprintln(cmd.OutOrStdout(), "  2) talon secrets migrate <path>       (one secret at a time)")
				fmt.Fprintln(cmd.OutOrStdout(), "  (migrate + keychain-bootstrap subcommands ship in the next commit)")
			}
			return nil
		},
	}
}

// auditSecrets walks the merged config and returns one entry per
// sensitive leaf. "Sensitive" is determined by the same key-name
// rule used by the redactor — that's the contract callers can
// rely on. Stable sort by path for reproducible output.
func auditSecrets(merged []byte) []secretsAuditEntry {
	out := []secretsAuditEntry{}
	walkSecretLeaves(gjson.ParseBytes(merged), "", &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// walkSecretLeaves recursively visits every leaf in v, recording
// secretsAuditEntry rows for keys that match the sensitive-key
// rule. Paths are dotted with [N] for array indices, matching
// `talon config get` syntax.
func walkSecretLeaves(v gjson.Result, prefix string, out *[]secretsAuditEntry) {
	if v.IsObject() {
		v.ForEach(func(key, val gjson.Result) bool {
			child := key.Str
			path := child
			if prefix != "" {
				path = prefix + "." + child
			}
			// Audit only string leaves — bools/numbers can match a
			// sensitive-key word (e.g. allowInsecureAuth) but
			// can't BE a credential. Skip them so the punch list
			// stays accurate.
			if secrets.IsSensitiveKey(child) && val.Type == gjson.String {
				*out = append(*out, classifyLeaf(path, val))
				return true
			}
			walkSecretLeaves(val, path, out)
			return true
		})
		return
	}
	if v.IsArray() {
		i := 0
		v.ForEach(func(_, val gjson.Result) bool {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			i++
			walkSecretLeaves(val, path, out)
			return true
		})
	}
}

func classifyLeaf(path string, v gjson.Result) secretsAuditEntry {
	s := v.String()
	if strings.TrimSpace(s) == "" {
		return secretsAuditEntry{Path: path, Kind: "empty", Value: "", Note: "configured but blank"}
	}
	if secrets.IsReference(s) {
		return secretsAuditEntry{Path: path, Kind: "ref", Value: s}
	}
	return secretsAuditEntry{Path: path, Kind: "literal", Value: secrets.Placeholder, Note: "move to 1Password or keychain"}
}
