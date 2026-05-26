package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/guygrigsby/talon/internal/agentcontext"
	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/connectapi"
	plugin "github.com/guygrigsby/talon/internal/plugin/host"
	"github.com/guygrigsby/talon/internal/plugin/native"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
)

func gatewayCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gateway",
		Short: "Run, inspect, and query the WebSocket Gateway",
	}
	c.AddCommand(gatewayCallCmd())
	c.AddCommand(gatewayDiagnosticsCmd())
	c.AddCommand(gatewayDiscoverCmd())
	c.AddCommand(gatewayHealthCmd())
	c.AddCommand(gatewayInstallCmd())
	c.AddCommand(gatewayProbeCmd())
	c.AddCommand(gatewayRestartCmd())
	c.AddCommand(gatewayRunCmd())
	c.AddCommand(gatewayStabilityCmd())
	c.AddCommand(gatewayStartCmd())
	c.AddCommand(gatewayStatusCmd())
	c.AddCommand(gatewayStopCmd())
	c.AddCommand(gatewayUninstallCmd())
	c.AddCommand(gatewayUsageCostCmd())
	return c
}

func notYetImplemented(issueID string) error {
	return fmt.Errorf("not yet implemented (tracked as %s)", issueID)
}

func gatewayRunCmd() *cobra.Command {
	var (
		port              int
		bind              string
		token             string
		password          string
		passwordFile      string
		auth              string
		webDir            string
		force             bool
		dev               bool
		reset             bool
		verbose           bool
		allowUnconfigured bool
		rawStream         bool
		rawStreamPath     string
		tailscale         string
		tailscaleReset    bool
		wsLog             string
		compact           bool
		cliBackendLogs    bool
	)
	c := &cobra.Command{
		Use:   "run",
		Short: "Run the WebSocket Gateway (foreground)",
		RunE: func(cmd *cobra.Command, args []string) error {
			for flag, val := range map[string]string{
				"--password":        password,
				"--password-file":   passwordFile,
				"--raw-stream-path": rawStreamPath,
			} {
				if val != "" {
					fmt.Fprintf(os.Stderr, "talon: %s accepted but not yet wired\n", flag)
				}
			}
			for flag, val := range map[string]bool{
				"--dev":                dev,
				"--reset":              reset,
				"--allow-unconfigured": allowUnconfigured,
				"--raw-stream":         rawStream,
				"--compact":            compact,
				"--cli-backend-logs":   cliBackendLogs,
			} {
				if val {
					fmt.Fprintf(os.Stderr, "talon: %s accepted but not yet wired\n", flag)
				}
			}

			// --tailscale: parse mode early so a typo aborts before
			// we spin up listeners. Actual exposure happens after
			// the gateway is listening (below).
			tsMode, err := parseTailscaleMode(tailscale)
			if err != nil {
				return err
			}
			_ = wsLog
			_ = verbose

			host := "127.0.0.1"
			switch bind {
			case "", "loopback":
				host = "127.0.0.1"
			case "lan", "auto":
				host = "0.0.0.0"
			default:
				host = "127.0.0.1"
				fmt.Fprintf(os.Stderr, "talon: --bind=%q not yet wired; using loopback\n", bind)
			}
			addr := fmt.Sprintf("%s:%d", host, port)

			if force {
				fmt.Fprintln(os.Stderr, "talon: --force accepted but kill-existing-listener not yet wired")
			}

			// Auth resolution order:
			//  1. --auth + --token CLI flags (explicit override)
			//  2. gateway.auth.{mode,token} from merged config,
			//     with the token routed through the secrets
			//     resolver so op:// / keychain:// references
			//     work the same as a literal value
			//  3. AuthNone fallback when nothing's configured
			//
			// Filling in from config closes the foot-gun the user
			// hit before: starting `gateway run` without --token
			// silently dropped to auth=none even though the
			// config had a token set, leaving a LAN-bound listener
			// open to anyone on the network.
			authMode := server.AuthMode(auth)
			if authMode == "" || token == "" {
				cfgPaths := resolvePaths()
				if merged, err := config.MergedBytes(cfgPaths); err == nil {
					if authMode == "" {
						if v := gjson.GetBytes(merged, "gateway.auth.mode"); v.Exists() && v.Str != "" {
							authMode = server.AuthMode(v.Str)
						}
					}
					if token == "" {
						if v := gjson.GetBytes(merged, "gateway.auth.token"); v.Exists() && v.Str != "" {
							resolveCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
							resolved, rerr := secrets.NewResolver().Resolve(resolveCtx, v.Str)
							cancel()
							if rerr != nil {
								return fmt.Errorf("resolve gateway.auth.token: %w", rerr)
							}
							token = resolved
						}
					}
				}
			}
			if authMode == "" {
				if token != "" {
					authMode = server.AuthToken
				} else {
					authMode = server.AuthNone
				}
			}
			if authMode == server.AuthToken && token == "" {
				return fmt.Errorf("--auth=token requires --token (or gateway.auth.token in config)")
			}
			// LAN-binding without auth is the canonical foot-gun;
			// loud warning so the user notices before someone else
			// on the network does.
			if authMode == server.AuthNone && (bind == "lan" || bind == "auto" || bind == "tailnet") {
				fmt.Fprintln(os.Stderr, "talon: WARNING — gateway is binding to "+bind+" with auth=none.")
				fmt.Fprintln(os.Stderr, "talon: anyone on the network can drive this gateway.")
				fmt.Fprintln(os.Stderr, "talon: set gateway.auth.token in config (`talon config set gateway.auth.token op://...`) or pass --token.")
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			paths := resolvePaths()
			resolver := &configAgentResolver{paths: paths}

			// First-run scaffolding: seed default persona files
			// (IDENTITY/SOUL/AGENTS/USER) when the main agent's
			// workspace has none, so a fresh install boots with an
			// identity instead of a blank, GPT-hallucinating agent.
			// Write-if-missing only — never clobbers customized files.
			// Targets the configured main workspace, falling back to
			// ~/.talon where the persona files live by default.
			mainWS := paths.Talon.Dir
			if ws, err := resolver.Workspace("main"); err == nil && ws != "" {
				mainWS = ws
			}
			if created, err := agentcontext.EnsureDefaults(mainWS); err != nil {
				slog.Warn("persona scaffolding failed", "dir", mainWS, "err", err)
			} else if len(created) > 0 {
				slog.Info("scaffolded default persona files", "dir", mainWS, "files", created)
			}

			pluginHost := plugin.NewHost()
			defer pluginHost.Shutdown()

			srv := server.New(server.Config{
				Addr:              addr,
				WebDir:            webDir,
				Auth:              server.AuthConfig{Mode: authMode, Token: token},
				AgentResolver:     resolver,
				ProviderFactory:   &agentProviderFactory{paths: paths, host: pluginHost},
				WorkspaceResolver: resolver,
				ToolRunnerFor:     newToolRunnerFactory(pluginHost, paths),
				Paths:             paths,
				PluginHost:        pluginHost,
			})

			// Memory sidecar (talon-2dn): opt-in via memory.enabled
			// in merged config. When on, constructs the pure-Go
			// GoMLX embedder + chromem-backed vector store under
			// ~/.talon/memory and wires them into ChatHandler. The
			// embedder downloads its model (~90MB MiniLM) from
			// HuggingFace on first chat.send — we don't pre-warm
			// here so a stuck network at startup doesn't block the
			// gateway. Storage path overridable via memory.path.
			mem := buildMemorySidecar(paths)
			if mem != nil {
				srv.ChatHandler().WithMemory(mem)
				slog.Info("memory sidecar wired",
					"store_path", filepath.Join(paths.Talon.Dir, "memory"),
				)
			}

			// Agentcore chat dispatch (Phase 3 of migration plan).
			// Wires the alternative chat-loop path through
			// internal/agentcore_chat. Selected per-call by model
			// provider; OpenAI and Anthropic currently stay on the
			// host provider path. Memory
			// sidecar (when present) is reused — same store +
			// recaller back the jess Remember/Recall tools the
			// agentcore agent sees.
			srv.ChatHandler().
				WithPaths(paths).
				WithAgentcoreRunner(buildAgentcoreRunner(paths, mem))

			// Connect API (talon-y6v): expose every RPC the WS
			// path serves over Connect (HTTP/JSON for browsers,
			// gRPC wire elsewhere) too. Both transports run in
			// parallel during the migration — frontends switch
			// per-call. Streaming RPCs (chat.Subscribe,
			// sessions.Subscribe) return Unimplemented on the
			// Connect path for now; the WS stream stays the
			// only path for chat events until stage 2 of
			// talon-y6v lands the EventSink refactor.
			connectapi.Register(srv.Mux(), srv)
			slog.Info("connect API wired",
				"prefix", "/talon.v1.*",
			)

			// Now that the WS server has built its handlers, wire the
			// plugin Host service against the SAME ChatStore /
			// SessionStore so plugins see the same view as the UI.
			hostService := server.NewHostService(paths, srv.ReadHandler(), srv.ChatHandler(), srv.ChatStore(), srv.SessionStore())
			nativeFactory := func(string, *native.ManifestHolder) pb.HostServer {
				return hostService
			}
			loadConfiguredPlugins(ctx, pluginHost, paths, nativeFactory)
			pluginNames := pluginHost.List()

			// Channel dispatchers wire each plugin-offered channel
			// referenced in channels.<name> into the gateway's chat
			// loop. Stops on gateway shutdown (defer below).
			channelDispatchers := startConfiguredChannels(ctx, pluginHost, paths, srv.ChatHandler())
			defer func() {
				for _, d := range channelDispatchers {
					d.Stop()
				}
			}()

			// Message text "talon gateway listening" is matched by the
			// e2e harness's wait-for-ready hook (internal/e2e). Don't
			// rename without updating that pattern.
			slog.Info("talon gateway listening",
				"addr", addr,
				"auth", authMode,
				"chat", "enabled",
				"providers", "openai/deepseek/lmstudio",
				"tools", "read/write/edit/bash/glob/grep/remember/subagent",
				"plugins", len(pluginNames),
				"channels", len(channelDispatchers),
			)
			// Forgettable-URL mitigation: print the deep-link the UI needs
			// after a fresh page load (cache cleared, new browser, etc).
			// Token is included only when --auth=token; the
			// fragment form keeps it out of HTTP request logs.
			gwHost := "localhost"
			if host == "0.0.0.0" {
				gwHost = "localhost" // 0.0.0.0 isn't dialable; show loopback
			}
			ui := buildUIURL(defaultUIHost, gwHost, port, token, "main", "/")
			// Log-safe form: redact the token's value so the URL
			// is still diagnostic but no credential lands in
			// stdout / Docker logs / terminal scrollback. The
			// real URL (with token) is what the user clicks
			// from `talon dashboard` — which copies to clipboard
			// and opens the browser, never logs.
			slog.Info("ui url", "url", logSafeURL(ui), "hint", "override host with: talon ui url --ui-host=...")

			// Crash-loop detection. Records this startup and, if
			// we've started 3+ times in 5 minutes, fires a
			// Telegram alert (when channels.telegram is configured).
			// Best-effort, never blocks startup.
			recordStartupAndAlert(ctx, paths)

			// Tailscale exposure: spin this up after the listener is
			// printed so the user sees the local URL even if the
			// CLI shell-out fails. Funnel/serve persist on the
			// Tailscale side, so the call can be best-effort.
			if tsMode != tailscaleOff {
				if err := applyTailscale(ctx, tsMode, port); err != nil {
					slog.Error("tailscale setup failed", "err", err)
				} else {
					slog.Info("tailscale active", "mode", tsMode, "port", port)
				}
				if tailscaleReset {
					defer resetTailscale(context.Background())
				}
			}
			return srv.Run(ctx)
		},
	}
	c.Flags().IntVar(&port, "port", 18789, "Port for the gateway WebSocket")
	c.Flags().StringVar(&bind, "bind", "", `Bind mode ("loopback"|"lan"|"tailnet"|"auto"|"custom")`)
	c.Flags().StringVar(&token, "token", "", "Shared token required in connect.params.auth.token")
	c.Flags().StringVar(&password, "password", "", "Password for auth mode=password")
	c.Flags().StringVar(&passwordFile, "password-file", "", "Read gateway password from file")
	c.Flags().StringVar(&auth, "auth", "", `Gateway auth mode ("none"|"token"|"password"|"trusted-proxy")`)
	c.Flags().StringVar(&webDir, "web", "", "Path to built control-ui dist dir (talon extension)")
	c.Flags().BoolVar(&force, "force", false, "Kill any existing listener on the target port before starting")
	c.Flags().BoolVar(&dev, "dev", false, "Create a dev config + workspace if missing")
	c.Flags().BoolVar(&reset, "reset", false, "Reset dev config + credentials + sessions + workspace (requires --dev)")
	c.Flags().BoolVar(&verbose, "verbose", false, "Verbose logging to stdout/stderr")
	c.Flags().BoolVar(&allowUnconfigured, "allow-unconfigured", false, "Allow gateway start without enforcing gateway.mode=local")
	c.Flags().BoolVar(&rawStream, "raw-stream", false, "Log raw model stream events to jsonl")
	c.Flags().StringVar(&rawStreamPath, "raw-stream-path", "", "Raw stream jsonl path")
	c.Flags().StringVar(&tailscale, "tailscale", "", `Tailscale exposure mode ("off"|"serve"|"funnel")`)
	c.Flags().BoolVar(&tailscaleReset, "tailscale-reset-on-exit", false, "Reset Tailscale serve/funnel configuration on shutdown")
	c.Flags().StringVar(&wsLog, "ws-log", "auto", `WebSocket log style ("auto"|"full"|"compact")`)
	c.Flags().BoolVar(&compact, "compact", false, `Alias for "--ws-log compact"`)
	c.Flags().BoolVar(&cliBackendLogs, "cli-backend-logs", false, "Only show CLI backend logs in the console")
	return c
}

func gatewayInstallCmd() *cobra.Command {
	var (
		force   bool
		jsonOut bool
		port    int
		runtm   string
		token   string
	)
	c := &cobra.Command{
		Use:   "install",
		Short: "Install the Gateway service (launchd/systemd/schtasks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = force
			_ = jsonOut
			_ = port
			_ = runtm
			_ = token
			return notYetImplemented("talon-4an")
		},
	}
	c.Flags().BoolVar(&force, "force", false, "Reinstall/overwrite if already installed")
	c.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	c.Flags().IntVar(&port, "port", 0, "Gateway port")
	c.Flags().StringVar(&runtm, "runtime", "go", "Daemon runtime (go)")
	c.Flags().StringVar(&token, "token", "", "Gateway token (token auth)")
	return c
}

func gatewayStartCmd() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "start",
		Short: "Start the Gateway service (launchd/systemd/schtasks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = jsonOut
			return notYetImplemented("talon-4an")
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	return c
}

func gatewayStopCmd() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "stop",
		Short: "Stop the Gateway service (launchd/systemd/schtasks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = jsonOut
			return notYetImplemented("talon-4an")
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	return c
}

func gatewayRestartCmd() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "restart",
		Short: "Restart the Gateway service (launchd/systemd/schtasks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = jsonOut
			return notYetImplemented("talon-4an")
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	return c
}

func gatewayUninstallCmd() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the Gateway service (launchd/systemd/schtasks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = jsonOut
			return notYetImplemented("talon-4an")
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	return c
}

func gatewayDiscoverCmd() *cobra.Command {
	var (
		jsonOut   bool
		timeoutMs int
	)
	c := &cobra.Command{
		Use:   "discover",
		Short: "Discover gateways via Bonjour (local + wide-area if configured)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = jsonOut
			_ = timeoutMs
			return notYetImplemented("talon-r0p")
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	c.Flags().IntVar(&timeoutMs, "timeout", 2000, "Per-command timeout in ms")
	return c
}

func gatewayDiagnosticsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "diagnostics",
		Short: "Export local support diagnostics",
	}
	c.AddCommand(gatewayDiagnosticsExportCmd())
	return c
}

func gatewayDiagnosticsExportCmd() *cobra.Command {
	opts := &diagnosticsExportOpts{}
	// urlFlag/tokenFlag/passwordFlag/noStabilityBundle are accepted but not
	// wired; diagnostics uses the shared config-driven dial path today.
	var (
		urlFlag           string
		tokenFlag         string
		passwordFlag      string
		noStabilityBundle bool
	)
	c := &cobra.Command{
		Use:   "export",
		Short: "Write a shareable, payload-free diagnostics .zip",
		RunE: func(cmd *cobra.Command, args []string) error {
			for flag, val := range map[string]string{
				"--url":      urlFlag,
				"--token":    tokenFlag,
				"--password": passwordFlag,
			} {
				if val != "" {
					fmt.Fprintf(os.Stderr, "talon: %s accepted but not yet wired (using config-driven dial)\n", flag)
				}
			}
			if noStabilityBundle {
				// Stability bundle integration is a separate item
				// (we don't write a bundle today) — flag is a no-op
				// for now, accept silently.
			}
			return diagnosticsExportRunE(*opts)
		},
	}
	c.Flags().StringVar(&opts.output, "output", "", "Output .zip path")
	c.Flags().IntVar(&opts.logLines, "log-lines", 5000, "Maximum sanitized log lines to include")
	c.Flags().IntVar(&opts.logBytes, "log-bytes", 1000000, "Maximum log bytes to inspect")
	c.Flags().StringVar(&urlFlag, "url", "", "Gateway WebSocket URL for health snapshot")
	c.Flags().StringVar(&tokenFlag, "token", "", "Gateway token for health snapshot")
	c.Flags().StringVar(&passwordFlag, "password", "", "Gateway password for health snapshot")
	c.Flags().IntVar(&opts.timeoutMs, "timeout", 3000, "Status/health snapshot timeout in ms (0 = skip)")
	c.Flags().BoolVar(&noStabilityBundle, "no-stability-bundle", false, "Skip persisted stability bundle lookup (no-op today)")
	c.Flags().BoolVar(&opts.jsonOut, "json", false, "Output JSON")
	return c
}

func gatewayCallCmd() *cobra.Command {
	var paramsFlag string
	c := &cobra.Command{
		Use:   "call <method> [--params JSON]",
		Short: "Call a Gateway RPC method",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var params any
			if strings.TrimSpace(paramsFlag) != "" {
				if err := json.Unmarshal([]byte(paramsFlag), &params); err != nil {
					return fmt.Errorf("--params not valid JSON: %w", err)
				}
			}
			payload, err := runRPC(args[0], params)
			if err != nil {
				return err
			}
			emit(payload)
			return nil
		},
	}
	c.Flags().StringVar(&paramsFlag, "params", "", "JSON params for the RPC")
	return c
}

func gatewayHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Fetch Gateway health",
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := runRPC("health", nil)
			if err != nil {
				return err
			}
			emit(payload)
			return nil
		},
	}
}

func gatewayStabilityCmd() *cobra.Command {
	var (
		limit    int
		typeFlag string
		sinceSeq int64
	)
	c := &cobra.Command{
		Use:   "stability",
		Short: "Fetch payload-free Gateway stability diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]any{"limit": limit}
			if typeFlag != "" {
				params["type"] = typeFlag
			}
			if cmd.Flags().Changed("since-seq") {
				params["sinceSeq"] = sinceSeq
			}
			payload, err := runRPC("diagnostics.stability", params)
			if err != nil {
				return err
			}
			emit(payload)
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 25, "Maximum number of recent events")
	c.Flags().StringVar(&typeFlag, "type", "", "Filter by diagnostic event type")
	c.Flags().Int64Var(&sinceSeq, "since-seq", 0, "Only include events after this sequence")
	return c
}

func gatewayUsageCostCmd() *cobra.Command {
	var days int
	c := &cobra.Command{
		Use:   "usage-cost",
		Short: "Fetch usage cost summary from session logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := runRPC("usage.cost", map[string]any{"days": days})
			if err != nil {
				return err
			}
			emit(payload)
			return nil
		},
	}
	c.Flags().IntVar(&days, "days", 30, "Number of days to include")
	return c
}

func gatewayProbeCmd() *cobra.Command {
	var timeoutMs int
	c := &cobra.Command{
		Use:   "probe",
		Short: "Show gateway reachability and auth capability",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
			defer cancel()

			start := time.Now()
			cli, cfg, err := dial(ctx)
			dialMs := time.Since(start).Milliseconds()
			if err != nil {
				out := map[string]any{
					"ok":        false,
					"url":       "",
					"dialMs":    dialMs,
					"error":     err.Error(),
					"hasToken":  false,
					"reachable": false,
				}
				if cfg != nil {
					out["url"] = cfg.GatewayURL()
					out["hasToken"] = cfg.Gateway.Auth.Token != ""
				}
				emitValue(out)
				return err
			}
			defer cli.Close()

			rpcStart := time.Now()
			payload, rpcErr := cli.Request(ctx, "health", nil)
			rpcMs := time.Since(rpcStart).Milliseconds()

			out := map[string]any{
				"ok":        rpcErr == nil,
				"url":       cfg.GatewayURL(),
				"hasToken":  cfg.Gateway.Auth.Token != "",
				"dialMs":    dialMs,
				"rpcMs":     rpcMs,
				"reachable": true,
			}
			if rpcErr != nil {
				out["error"] = rpcErr.Error()
			} else {
				var health any
				if err := json.Unmarshal(payload, &health); err == nil {
					out["health"] = health
				}
			}
			emitValue(out)
			return rpcErr
		},
	}
	c.Flags().IntVar(&timeoutMs, "timeout", 3000, "Overall probe budget in ms")
	return c
}

func gatewayStatusCmd() *cobra.Command {
	var (
		urlFlag      string
		tokenFlag    string
		passwordFlag string
		timeoutMs    int
		deep         bool
		noProbe      bool
		requireRPC   bool
	)
	c := &cobra.Command{
		Use:   "status",
		Short: "Show gateway service status + probe connectivity/capability",
		RunE: func(cmd *cobra.Command, args []string) error {
			if passwordFlag != "" {
				fmt.Fprintln(os.Stderr, "talon: --password not supported (token auth only)")
			}

			out := map[string]any{
				"probed":  !noProbe,
				"service": gatewayServiceProbe(deep),
			}

			if noProbe {
				cfg, err := config.Load(resolvePaths())
				if err != nil {
					out["error"] = err.Error()
					emitValue(out)
					return err
				}
				url := cfg.GatewayURL()
				if urlFlag != "" {
					url = urlFlag
				}
				out["url"] = url
				out["hasToken"] = cfg.Gateway.Auth.Token != "" || tokenFlag != ""
				emitValue(out)
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
			defer cancel()

			start := time.Now()
			cli, cfg, err := dialWith(ctx, urlFlag, tokenFlag)
			dialMs := time.Since(start).Milliseconds()
			if err != nil {
				out["ok"] = false
				out["dialMs"] = dialMs
				out["error"] = err.Error()
				out["reachable"] = false
				if cfg != nil {
					out["url"] = cfg.GatewayURL()
				}
				emitValue(out)
				if requireRPC {
					return err
				}
				return nil
			}
			defer cli.Close()

			rpcStart := time.Now()
			payload, rpcErr := cli.Request(ctx, "health", nil)
			rpcMs := time.Since(rpcStart).Milliseconds()

			out["url"] = cfg.GatewayURL()
			out["hasToken"] = cfg.Gateway.Auth.Token != "" || tokenFlag != ""
			out["dialMs"] = dialMs
			out["rpcMs"] = rpcMs
			out["reachable"] = true
			out["ok"] = rpcErr == nil
			if rpcErr != nil {
				out["error"] = rpcErr.Error()
			} else {
				var health any
				if err := json.Unmarshal(payload, &health); err == nil {
					out["health"] = health
				}
			}
			emitValue(out)
			if requireRPC && rpcErr != nil {
				return rpcErr
			}
			return nil
		},
	}
	c.Flags().StringVar(&urlFlag, "url", "", "Gateway WebSocket URL (defaults to config)")
	c.Flags().StringVar(&tokenFlag, "token", "", "Gateway token (overrides config)")
	c.Flags().StringVar(&passwordFlag, "password", "", "Gateway password (not supported by talon)")
	c.Flags().IntVar(&timeoutMs, "timeout", 10000, "Timeout in ms")
	c.Flags().BoolVar(&deep, "deep", false, "Scan system-level services")
	c.Flags().BoolVar(&noProbe, "no-probe", false, "Skip RPC probe")
	c.Flags().BoolVar(&requireRPC, "require-rpc", false, "Exit non-zero when the RPC probe fails")
	return c
}

func gatewayServiceProbe(deep bool) map[string]any {
	if !deep {
		return map[string]any{"scanned": false}
	}
	switch runtime.GOOS {
	case "darwin":
		out, _ := exec.Command("launchctl", "list").Output()
		var matches []string
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(strings.ToLower(line), "talon") {
				matches = append(matches, strings.TrimSpace(line))
			}
		}
		return map[string]any{"scanned": true, "platform": "darwin", "manager": "launchctl", "matches": matches}
	case "linux":
		out, _ := exec.Command("systemctl", "--user", "list-units", "--no-legend", "--no-pager").Output()
		var matches []string
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(strings.ToLower(line), "talon") {
				matches = append(matches, strings.TrimSpace(line))
			}
		}
		return map[string]any{"scanned": true, "platform": "linux", "manager": "systemd", "matches": matches}
	default:
		return map[string]any{"scanned": false, "platform": runtime.GOOS, "manager": "unknown"}
	}
}

func emitValue(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Println(v)
		return
	}
	fmt.Println(string(b))
}
