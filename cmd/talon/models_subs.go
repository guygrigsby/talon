// Package main implements `talon models` subcommands beyond `list`.
//
// Each subcommand here is a thin wrapper over config writes:
//   - models set <model>:           agents.defaults.model.primary = <model>
//   - models fallbacks list/add/remove/clear: agents.defaults.model.fallbacks
//   - models aliases  list/add/remove:        agents.defaults.models.<id>.alias
//
// Live probes (`models status --probe`) and OpenRouter scans (`models scan`)
// require additional infrastructure and are deferred.

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

// --- models set -----------------------------------------------------------

func modelsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <model>",
		Short: "Set the default model (agents.defaults.model.primary)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model := strings.TrimSpace(args[0])
			if model == "" {
				return fmt.Errorf("models set: <model> is required")
			}
			paths := resolvePaths()
			segments := []string{"agents", "defaults", "model", "primary"}
			if _, err := config.Set(paths, segments, model, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
				return fmt.Errorf("models set: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "set agents.defaults.model.primary =", model)
			emitReloadHint(cmd, segments)
			return nil
		},
	}
}

// --- models fallbacks -----------------------------------------------------

func modelsFallbacksCmd() *cobra.Command {
	c := &cobra.Command{Use: "fallbacks", Short: "Manage agents.defaults.model.fallbacks"}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured fallback models",
		RunE: func(cmd *cobra.Command, args []string) error {
			fallbacks, err := readFallbacks()
			if err != nil {
				return err
			}
			if flagJSON {
				out, _ := json.Marshal(fallbacks)
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			if len(fallbacks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No fallback models configured.")
				return nil
			}
			for i, m := range fallbacks {
				fmt.Fprintf(cmd.OutOrStdout(), "%d. %s\n", i+1, m)
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "add <model>",
		Short: "Append a fallback model (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model := strings.TrimSpace(args[0])
			if model == "" {
				return fmt.Errorf("models fallbacks add: <model> is required")
			}
			fallbacks, err := readFallbacks()
			if err != nil {
				return err
			}
			for _, existing := range fallbacks {
				if existing == model {
					fmt.Fprintf(cmd.OutOrStdout(), "%s is already a fallback (no change)\n", model)
					return nil
				}
			}
			fallbacks = append(fallbacks, model)
			return writeFallbacks(cmd, fallbacks)
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "remove <model>",
		Short: "Remove a fallback model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model := strings.TrimSpace(args[0])
			fallbacks, err := readFallbacks()
			if err != nil {
				return err
			}
			next := fallbacks[:0]
			removed := false
			for _, m := range fallbacks {
				if m == model {
					removed = true
					continue
				}
				next = append(next, m)
			}
			if !removed {
				return fmt.Errorf("models fallbacks remove: %q not in fallbacks", model)
			}
			return writeFallbacks(cmd, next)
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear all fallback models",
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeFallbacks(cmd, []string{})
		},
	})
	return c
}

func readFallbacks() ([]string, error) {
	paths := resolvePaths()
	merged, err := config.MergedBytes(paths)
	if err != nil {
		return nil, fmt.Errorf("read merged config: %w", err)
	}
	return parseFallbacksFromBytes(merged), nil
}

// parseFallbacksFromBytes is the gjson-only piece of readFallbacks,
// extracted for unit-testing without resolvePaths()'s global flag
// dependency.
func parseFallbacksFromBytes(merged []byte) []string {
	v := gjson.GetBytes(merged, "agents.defaults.model.fallbacks")
	if !v.Exists() || !v.IsArray() {
		return nil
	}
	out := []string{}
	v.ForEach(func(_, val gjson.Result) bool {
		if s := strings.TrimSpace(val.String()); s != "" {
			out = append(out, s)
		}
		return true
	})
	return out
}

func writeFallbacks(cmd *cobra.Command, list []string) error {
	paths := resolvePaths()
	value := make([]any, len(list))
	for i, m := range list {
		value[i] = m
	}
	segments := []string{"agents", "defaults", "model", "fallbacks"}
	if _, err := config.Set(paths, segments, value, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
		return fmt.Errorf("write fallbacks: %w", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "cleared agents.defaults.model.fallbacks")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "set agents.defaults.model.fallbacks = [%s]\n", strings.Join(list, ", "))
	}
	emitReloadHint(cmd, segments)
	return nil
}

// --- models aliases -------------------------------------------------------
//
// Aliases live at agents.defaults.models.<modelId>.alias = <name>. The
// model id is the canonical "<provider>/<model>" form; the alias is a
// short name the user picks (e.g. "fast" or "smart"). Both
// `agents.list[].model` and CLI/UI selectors can resolve the alias
// back to the model id at runtime.

func modelsAliasesCmd() *cobra.Command {
	c := &cobra.Command{Use: "aliases", Short: "Manage model aliases (agents.defaults.models.<id>.alias)"}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured model aliases",
		RunE: func(cmd *cobra.Command, args []string) error {
			pairs, err := readAliases()
			if err != nil {
				return err
			}
			if flagJSON {
				out, _ := json.Marshal(pairs)
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			if len(pairs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No model aliases configured.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ALIAS\tMODEL")
			// Sort by alias for stable output.
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].Alias < pairs[j].Alias })
			for _, p := range pairs {
				fmt.Fprintf(tw, "%s\t%s\n", p.Alias, p.Model)
			}
			return tw.Flush()
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "add <alias> <model>",
		Short: "Set an alias for a model id",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := strings.TrimSpace(args[0])
			model := strings.TrimSpace(args[1])
			if alias == "" || model == "" {
				return fmt.Errorf("models aliases add: <alias> and <model> are required")
			}
			paths := resolvePaths()
			segments := []string{"agents", "defaults", "models", model, "alias"}
			if _, err := config.Set(paths, segments, alias, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
				return fmt.Errorf("models aliases add: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s as alias for %s\n", alias, model)
			emitReloadHint(cmd, segments)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "remove <alias>",
		Short: "Remove a model alias",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := strings.TrimSpace(args[0])
			pairs, err := readAliases()
			if err != nil {
				return err
			}
			var hit *aliasPair
			for i := range pairs {
				if pairs[i].Alias == alias {
					hit = &pairs[i]
					break
				}
			}
			if hit == nil {
				return fmt.Errorf("models aliases remove: %q not found", alias)
			}
			paths := resolvePaths()
			// Unset only the alias subfield — keep the rest of the
			// model entry (e.g. user-set fast-mode flags) intact.
			segments := []string{"agents", "defaults", "models", hit.Model, "alias"}
			if err := config.Unset(paths, segments); err != nil {
				return fmt.Errorf("models aliases remove: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed alias %s (was %s)\n", alias, hit.Model)
			emitReloadHint(cmd, segments)
			return nil
		},
	})
	return c
}

type aliasPair struct {
	Alias string `json:"alias"`
	Model string `json:"model"`
}

func readAliases() ([]aliasPair, error) {
	paths := resolvePaths()
	merged, err := config.MergedBytes(paths)
	if err != nil {
		return nil, fmt.Errorf("read merged config: %w", err)
	}
	return parseAliasesFromBytes(merged), nil
}

// parseAliasesFromBytes mirrors readAliases minus the path-load.
// Object-key iteration order isn't stable; callers that need stable
// ordering should sortAliases.
func parseAliasesFromBytes(merged []byte) []aliasPair {
	v := gjson.GetBytes(merged, "agents.defaults.models")
	out := []aliasPair{}
	if !v.Exists() || !v.IsObject() {
		return out
	}
	v.ForEach(func(modelKey, modelVal gjson.Result) bool {
		alias := strings.TrimSpace(modelVal.Get("alias").Str)
		if alias == "" {
			return true
		}
		out = append(out, aliasPair{Alias: alias, Model: modelKey.Str})
		return true
	})
	return out
}

func sortAliases(p []aliasPair) {
	sort.Slice(p, func(i, j int) bool { return p[i].Alias < p[j].Alias })
}

// emitReloadHint prints the per-path reload class hint after a
// write, matching `talon config set` UX. The reload class is
// classified per-path; the hint string interpolates the dotted
// path so the message is self-explanatory.
func emitReloadHint(cmd *cobra.Command, segments []string) {
	class := config.ClassifyReload(segments)
	hint := class.Hint(strings.Join(segments, "."))
	if hint == "" {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), hint)
}
