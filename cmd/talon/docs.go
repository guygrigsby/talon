package main

// `talon docs` prints links into Talon's in-repo docs.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

const docsBaseURL = "https://github.com/guygrigsby/talon/tree/main/docs"

func docsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "docs [query...]",
		Short: "Open or search the Talon docs",
		Long: `Print a link to the Talon docs, or when given a query print a
GitHub search link scoped to the docs directory.

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
			searchURL := "https://github.com/guygrigsby/talon/search?q=" + url.QueryEscape(query) + "&type=code"
			fmt.Fprintf(cmd.OutOrStdout(), "Search: %s\n", searchURL)
			return nil
		},
	}
	return c
}
