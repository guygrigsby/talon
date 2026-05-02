package main

// `talon docs` — minimal port of openclaw's docs-search command.
//
// openclaw shells out to `mcporter call <SEARCH_TOOL> ...` to hit a
// hosted MCP search endpoint. That's a runtime dep we'd rather not
// take on yet (talon-578, plus it's openclaw's gated server). The v0
// version surfaces the docs URL and a clickable per-query search
// link; if/when we want richer in-terminal results we can add the
// MCP shell-out behind a flag.
//
// Output style mirrors the openclaw command's no-args branch so
// muscle-memory ports cleanly between the two CLIs.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

const docsBaseURL = "https://docs.openclaw.ai"

func docsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "docs [query...]",
		Short: "Open or search the OpenClaw docs",
		Long: `Print a link to the OpenClaw docs site, or — when given a query —
a search link with the query pre-filled.

  talon docs                  # show the docs URL
  talon docs gateway auth     # search link for "gateway auth"`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.TrimSpace(strings.Join(args, " "))
			if query == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Docs:   "+docsBaseURL+"/")
				fmt.Fprintln(cmd.OutOrStdout(), "Search: talon docs \"your query\"")
				return nil
			}
			searchURL := docsBaseURL + "/?q=" + url.QueryEscape(query)
			fmt.Fprintf(cmd.OutOrStdout(), "Search: %s\n", searchURL)
			return nil
		},
	}
	return c
}
