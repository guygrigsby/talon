package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/spf13/cobra"
)

// defaultUIHost is empty by default, signaling "open the UI the
// gateway serves itself" (the embedded SvelteKit SPA at
// http://<gw-host>:<gw-port>/). Override with --ui-host to point
// at a separate dev server (e.g. `make dev` runs vite at
// http://localhost:5173 which proxies /talon.v1.* + /ws back to
// the gateway).
const defaultUIHost = ""

// uiCmd parents `talon ui url` and `talon ui open`.
func uiCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ui",
		Short: "Generate a deep-link URL into the talon web UI for this gateway",
		Long: `Build (or open) a URL that pre-configures the talon web UI to talk
to this talon gateway.

By default the URL points at the embedded SPA served from the gateway's
own port (same origin). Pass --ui-host http://localhost:5173 to target
the Vite dev server during ` + "`make dev`" + ` iteration. Pass --token if the
gateway is running with --auth=token. Pass --session to route directly
to a chat session.

The token lands in the URL fragment (#token=...) so it never appears in
server logs or referrers; the FE reads it client-side and forwards it
as Authorization: Bearer ... on every Connect call.`,
	}
	c.AddCommand(uiURLCmd())
	c.AddCommand(uiOpenCmd())
	return c
}

func uiURLCmd() *cobra.Command {
	var (
		uiHost   string
		gwHost   string
		gwPort   int
		token    string
		session  string
		path     string
	)
	c := &cobra.Command{
		Use:   "url",
		Short: "Print the deep-link URL (no browser launch)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(buildUIURL(uiHost, gwHost, gwPort, token, session, path))
			return nil
		},
	}
	addUIFlags(c, &uiHost, &gwHost, &gwPort, &token, &session, &path)
	return c
}

func uiOpenCmd() *cobra.Command {
	var (
		uiHost   string
		gwHost   string
		gwPort   int
		token    string
		session  string
		path     string
		printOnly bool
	)
	c := &cobra.Command{
		Use:   "open",
		Short: "Print and open the deep-link URL in the default browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			u := buildUIURL(uiHost, gwHost, gwPort, token, session, path)
			fmt.Println(u)
			if printOnly {
				return nil
			}
			if err := openInBrowser(u); err != nil {
				fmt.Fprintf(os.Stderr, "talon: could not auto-open (%v); copy the URL above\n", err)
			}
			return nil
		},
	}
	addUIFlags(c, &uiHost, &gwHost, &gwPort, &token, &session, &path)
	c.Flags().BoolVar(&printOnly, "print-only", false, "print the URL but don't try to launch a browser")
	return c
}

func addUIFlags(c *cobra.Command, uiHost, gwHost *string, gwPort *int, token, session, path *string) {
	c.Flags().StringVar(uiHost, "ui-host", defaultUIHost, "UI base URL (empty = gateway's own URL; e.g. http://localhost:5173 for Vite dev)")
	c.Flags().StringVar(gwHost, "gateway-host", "localhost", "host the UI should dial for the gateway")
	c.Flags().IntVar(gwPort, "gateway-port", 18789, "port the UI should dial for the gateway")
	c.Flags().StringVar(token, "token", "", "gateway auth token (for --auth=token gateways)")
	c.Flags().StringVar(session, "session", "", "session key to open (e.g. main)")
	c.Flags().StringVar(path, "path", "/", "UI route to open (e.g. /, /agents)")
}

// buildUIURL composes the deep-link form:
//
//	<uiHost><path>?session=<s>#token=<encoded>
//
// Token lands in the fragment so it never appears in server logs or
// referrers; the FE reads it client-side and forwards as
// Authorization: Bearer ... on every Connect call.
//
// When uiHost is empty (the default), the URL targets the embedded
// SPA at http://<gwHost>:<gwPort>/ — same origin as the gateway. Pass
// an explicit --ui-host to target a separate dev server (e.g. Vite
// at http://localhost:5173 during `make dev`).
func buildUIURL(uiHost, gwHost string, gwPort int, token, session, routePath string) string {
	if uiHost == "" {
		uiHost = "http://" + gwHost + ":" + strconv.Itoa(gwPort)
	}

	frag := url.Values{}
	if token != "" {
		frag.Set("token", token)
	}

	q := ""
	if session != "" {
		q = "?session=" + url.QueryEscape(session)
	}

	base := uiHost + routePath + q
	if len(frag) == 0 {
		return base
	}
	return base + "#" + frag.Encode()
}

// openInBrowser launches the system default browser for u. Best-effort;
// returns the underlying error if the launch helper isn't available.
func openInBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
