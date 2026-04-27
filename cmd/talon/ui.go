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

// defaultUIHost is where the openclaw web UI's vite dev server runs by
// convention. Override per-command with --ui-host.
const defaultUIHost = "http://localhost:5173"

// uiCmd parents `talon ui url` and `talon ui open`.
func uiCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ui",
		Short: "Generate a deep-link URL into the openclaw web UI for this gateway",
		Long: `Build (or open) a URL that pre-configures the openclaw web UI to talk
to this talon gateway. The UI host defaults to ` + defaultUIHost + ` (vite
dev server); override with --ui-host. Pass --token if the gateway is
running with --auth=token. Pass --session to route directly to a chat
session.

The generated URL uses a fragment-based fragment (#gatewayUrl=...&token=...)
so the token never appears in server logs or referrers.`,
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
	c.Flags().StringVar(uiHost, "ui-host", defaultUIHost, "openclaw web UI base URL")
	c.Flags().StringVar(gwHost, "gateway-host", "localhost", "host the UI should dial for the gateway")
	c.Flags().IntVar(gwPort, "gateway-port", 18790, "port the UI should dial for the gateway")
	c.Flags().StringVar(token, "token", "", "gateway auth token (for --auth=token gateways)")
	c.Flags().StringVar(session, "session", "", "session key to open (e.g. main)")
	c.Flags().StringVar(path, "path", "/chat", "UI route to open (e.g. /chat, /agents)")
}

// buildUIURL composes the deep-link form:
//
//	<uiHost><path>?session=<s>#gatewayUrl=<encoded>&token=<encoded>
//
// Token + gatewayUrl land in the fragment to avoid showing up in HTTP
// request logs and referrers. session goes in the query so refreshes keep
// the user on the same conversation.
//
// The gatewayUrl fragment is omitted when it would just restate the UI's
// own default — port 18789 on the UI's hostname. Concretely: running
// talon-gateway on the canonical openclaw port (18789) on localhost while
// the UI runs at http://localhost:5173 yields a clean URL with only
// ?session=... and no fragment.
func buildUIURL(uiHost, gwHost string, gwPort int, token, session, routePath string) string {
	wsURL := "ws://" + gwHost + ":" + strconv.Itoa(gwPort)

	frag := url.Values{}
	if !gatewayMatchesUIDefault(uiHost, gwHost, gwPort) {
		frag.Set("gatewayUrl", wsURL)
	}
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

// gatewayMatchesUIDefault reports whether ws://<gwHost>:<gwPort> equals
// the UI's implicit default (ws://<ui-hostname>:18789). Loopback aliases
// (localhost / 127.0.0.1) are treated as equivalent.
func gatewayMatchesUIDefault(uiHost, gwHost string, gwPort int) bool {
	if gwPort != 18789 {
		return false
	}
	u, err := url.Parse(uiHost)
	if err != nil {
		return false
	}
	uiHostname := u.Hostname()
	if uiHostname == "" {
		return false
	}
	return sameHostname(uiHostname, gwHost)
}

func sameHostname(a, b string) bool {
	if a == b {
		return true
	}
	loopback := func(s string) bool { return s == "localhost" || s == "127.0.0.1" || s == "::1" }
	return loopback(a) && loopback(b)
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
