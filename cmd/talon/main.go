package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/gateway"
	talonlog "github.com/guygrigsby/talon/internal/log"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/spf13/cobra"
)

var (
	flagTalonConfig    string
	flagOpenclawConfig string
	flagNoFallback     bool
	flagJSON           bool
	flagLogFormat      string
)

// resolvePaths returns the layered paths for this invocation, applying any
// global --config / --openclaw-config / --no-openclaw-fallback overrides.
func resolvePaths() openclaw.Paths {
	p := openclaw.DefaultPaths()
	if flagTalonConfig != "" {
		p.Talon.Config = flagTalonConfig
	}
	if flagOpenclawConfig != "" {
		p.Openclaw.Config = flagOpenclawConfig
	}
	if flagNoFallback {
		p.SkipOpenclaw = true
	}
	return p
}

// talonVersion is the version string emitted by `talon version` and
// `talon -V`/`--version`. Bumped manually for now; release tooling
// (talon-87g/talon-u9x) will inject via -ldflags later.
const talonVersion = "0.1.0-dev"

func main() {
	root := &cobra.Command{
		Use:   "talon",
		Short: "Fast openclaw-compatible gateway client",
		// Version drives Cobra's auto-generated -v/--version flag
		// (we bind -V manually below since openclaw uses -V). Format
		// matches `talon version` for byte-equivalence.
		Version: "talon " + talonVersion,
	}
	// openclaw's commander uses -V (capital), not -v. Match that for
	// drop-in alias parity. SetVersionTemplate strips the default
	// "{name} version {version}" wrapping so the output matches our
	// `version` subcommand exactly.
	root.SetVersionTemplate("{{.Version}}\n")
	root.Flags().BoolP("version", "V", false, "Print version and exit")

	root.PersistentFlags().StringVar(&flagTalonConfig, "config", "", "path to the talon overlay config (default: $TALON_CONFIG_PATH or ~/.talon/openclaw.json)")
	root.PersistentFlags().StringVar(&flagOpenclawConfig, "openclaw-config", "", "path to the read-only openclaw config (default: $OPENCLAW_CONFIG_PATH or ~/.openclaw/openclaw.json)")
	root.PersistentFlags().BoolVar(&flagNoFallback, "no-openclaw-fallback", false, "ignore the openclaw config layer when reading")
	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "emit raw JSON response")
	root.PersistentFlags().StringVar(&flagLogFormat, "log-format", "", "log handler: text (default, ANSI-colored on TTY) or json. Env override: TALON_LOG_FORMAT")

	// Wire the structured logger before any other init runs so
	// PersistentPreRun and the cobra-RunE bodies all share the same
	// pipeline. PersistentPreRunE fires after flag parsing, so the
	// --log-format flag is final by the time we read it.
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		f := flagLogFormat
		if f == "" {
			f = os.Getenv("TALON_LOG_FORMAT")
		}
		talonlog.Init(talonlog.ParseFormat(f))
		// Propagate to plugin subprocesses: spawned plugins inherit
		// our env, so writing TALON_LOG_FORMAT here makes the flag
		// take effect in every child plugin's talonlog.Init call.
		if f != "" {
			_ = os.Setenv("TALON_LOG_FORMAT", f)
		}
		return nil
	}

	root.AddCommand(versionCmd())
	root.AddCommand(healthCmd())
	root.AddCommand(gatewayCmd())
	root.AddCommand(configCmd())
	root.AddCommand(configureCmd())
	root.AddCommand(modelsCmd())
	root.AddCommand(agentsCmd())
	root.AddCommand(chatCmd())
	root.AddCommand(chatHistoryCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(uiCmd())
	root.AddCommand(docsCmd())
	root.AddCommand(secretsCmd())
	root.AddCommand(dashboardCmd())
	root.AddCommand(cronCmd())
	root.AddCommand(imagesCmd())

	// closeSharedRPC is wired as the post-run hook so any RPC client
	// the command branches lazily opened gets its WS shut cleanly
	// regardless of success vs error path. Cobra fires
	// PersistentPostRunE only on RunE-style commands; defer covers
	// the rest (and is idempotent).
	root.PersistentPostRunE = func(*cobra.Command, []string) error {
		closeSharedRPC()
		return nil
	}
	defer closeSharedRPC()

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "talon:", err)
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print talon client version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("talon " + talonVersion)
		},
	}
}

// dialFn is the dialer the rest of the CLI uses. Indirected through
// a package-level var so tests can substitute a stub without spinning
// up a real WS server.
var dialFn = dial

func dial(ctx context.Context) (*gateway.Client, *config.Config, error) {
	return dialWith(ctx, "", "")
}

// rpcConn is the shared RPC connection lazily-opened by sharedRPC()
// and reused for every subsequent runRPC in the same process.
//
// Why share it at all: the WS handshake is a connect-challenge
// round-trip per dial, ~2× the cost of a single RPC. Commands that
// fire a single RPC see no benefit, but multi-RPC commands and the
// future interactive REPL (talon-yct) save the per-call handshake.
//
// Lifecycle: opened on first sharedRPC() call, kept alive until
// closeSharedRPC() runs from cobra's PersistentPostRunE in main().
// dialOnce ensures concurrent first-callers don't race-dial; once
// rpcConn.cli is non-nil we skip the lock entirely on the hot path.
var rpcConn struct {
	mu      sync.Mutex
	cli     *gateway.Client
	cfg     *config.Config
	dialErr error
	dialed  bool
	// dialCount is exposed for tests; ordinary code reads neither
	// the field nor sharedRPC's caching state.
	dialCount int
}

func sharedRPC(ctx context.Context) (*gateway.Client, *config.Config, error) {
	rpcConn.mu.Lock()
	defer rpcConn.mu.Unlock()
	if rpcConn.dialed {
		return rpcConn.cli, rpcConn.cfg, rpcConn.dialErr
	}
	cli, cfg, err := dialFn(ctx)
	rpcConn.dialed = true
	rpcConn.dialCount++
	rpcConn.cli = cli
	rpcConn.cfg = cfg
	rpcConn.dialErr = err
	return cli, cfg, err
}

// closeSharedRPC tears down the shared RPC connection if one was
// opened. Idempotent — safe to call from both the cobra
// post-run hook and the deferred fallback in main().
func closeSharedRPC() {
	rpcConn.mu.Lock()
	defer rpcConn.mu.Unlock()
	if rpcConn.cli != nil {
		_ = rpcConn.cli.Close()
	}
	rpcConn.cli = nil
	rpcConn.cfg = nil
	rpcConn.dialErr = nil
	rpcConn.dialed = false
	// dialCount is left intact so tests can introspect the cumulative
	// open count across a sequence of calls within one test.
}

func dialWith(ctx context.Context, urlOverride, tokenOverride string) (*gateway.Client, *config.Config, error) {
	cfg, err := config.Load(resolvePaths())
	if err != nil {
		return nil, nil, err
	}
	url := cfg.GatewayURL()
	if urlOverride != "" {
		url = urlOverride
	}
	token := cfg.Gateway.Auth.Token
	if tokenOverride != "" {
		token = tokenOverride
	}
	cli := gateway.NewClient(url, token)
	if err := cli.Connect(ctx); err != nil {
		return nil, nil, err
	}
	return cli, cfg, nil
}

func runRPC(method string, params any) (json.RawMessage, error) {
	// dialCtx caps the connect handshake; rpcCtx is the per-call
	// budget. Splitting them lets the cached connection outlive any
	// single RPC's deadline without holding the WS open beyond the
	// process exit (closeSharedRPC handles that).
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()
	cli, _, err := sharedRPC(dialCtx)
	if err != nil {
		return nil, err
	}
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rpcCancel()
	return cli.Request(rpcCtx, method, params)
}

// emit prints payload as 2-space-indented JSON. Used as the default for
// commands without a typed renderer, and as the `--json` fallback for ones
// that have one.
func emit(payload json.RawMessage) {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		fmt.Println(string(payload))
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func healthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check gateway health",
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

func configCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Inspect and edit the layered openclaw config (no gateway)"}

	var fileAll bool
	fileCmd := &cobra.Command{
		Use:   "file",
		Short: "Print the talon overlay path (or both layers with --all)",
		Run: func(cmd *cobra.Command, args []string) {
			p := resolvePaths()
			if fileAll {
				fmt.Printf("talon:    %s\n", p.Talon.Config)
				if !p.SkipOpenclaw {
					fmt.Printf("openclaw: %s\n", p.Openclaw.Config)
				}
				return
			}
			fmt.Println(p.Talon.Config)
		},
	}
	fileCmd.Flags().BoolVar(&fileAll, "all", false, "print both the talon and openclaw paths")
	c.AddCommand(fileCmd)

	var (
		rawFlag    bool
		revealFlag bool
	)
	getCmd := &cobra.Command{
		Use:   "get <path>",
		Short: "Get a value from the merged config (talon overrides openclaw)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			segments, err := config.ParsePath(args[0])
			if err != nil {
				return err
			}
			res, err := config.Get(resolvePaths(), segments)
			if err != nil {
				return err
			}
			if !res.Exists() {
				return fmt.Errorf("path not found: %s", args[0])
			}
			out, redacted := renderConfigGetValue(args[0], res, rawFlag, revealFlag)
			fmt.Println(out)
			if redacted {
				fmt.Fprintln(os.Stderr, "talon: redacted sensitive value (use --reveal to show)")
			}
			return nil
		},
	}
	getCmd.Flags().BoolVar(&rawFlag, "raw", false, "print raw JSON value (don't unwrap strings)")
	getCmd.Flags().BoolVar(&revealFlag, "reveal", false, "show sensitive values (token/password/apiKey/...) instead of [REDACTED]")
	c.AddCommand(getCmd)

	var (
		strictJSON  bool
		legacyJSON  bool
		dryRun      bool
		mergeFlag   bool
		replaceAll  bool
		reloadFlag  string
	)
	setCmd := &cobra.Command{
		Use:   "set <path> <value>",
		Short: "Set a config value by path (atomic file edit)",
		Long: `Set a config value at <path>. Writes always target the talon overlay
(~/.talon/openclaw.json); the openclaw layer is read-only.

<value> is parsed as JSON when possible and falls back to a raw string. With
--strict-json, value must be valid JSON.

Path syntax mirrors openclaw: dot-separated, with [N] for array indices and
["key"] for keys containing dots or other reserved characters. Example paths:

  gateway.port
  agents.list[1].model
  channels.telegram.groups["*"].requireMention

The protected-path guard refuses to replace a protected map/list
(agents.defaults.models, models.providers[.<id>], plugins.entries,
auth.profiles, agents.list, models.providers.<id>.models) if your write
would shadow or drop entries from the merged view. Pass --merge to layer
additively, or --replace to bypass the guard. Note: --replace cannot delete
entries that come from the openclaw layer (it's read-only); the merged view
will continue to show those entries.

talon never auto-restarts the gateway. After a successful set, the output
includes a class-aware hint about whether you need to do anything (next-rpc:
no action; hup: SIGHUP the gateway; restart: full restart). Use --reload to
override the registry's classification for paths it doesn't know about.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawPath, val := args[0], args[1]
			segments, err := config.ParsePath(rawPath)
			if err != nil {
				return err
			}
			if len(segments) == 0 {
				return fmt.Errorf("path is empty")
			}
			if mergeFlag && replaceAll {
				return fmt.Errorf("choose either --merge or --replace, not both")
			}

			strict := strictJSON || legacyJSON
			var typed any
			if strict {
				if err := json.Unmarshal([]byte(val), &typed); err != nil {
					return fmt.Errorf("--strict-json given but value is not valid JSON: %w", err)
				}
			} else {
				if err := json.Unmarshal([]byte(val), &typed); err != nil {
					typed = val
				}
			}

			mode := config.SetReplaceSafe
			if mergeFlag {
				mode = config.SetMerge
			} else if replaceAll {
				mode = config.SetForceReplace
			}

			res, err := config.Set(resolvePaths(), segments, typed, config.SetOpts{Mode: mode, DryRun: dryRun})
			if err != nil {
				return err
			}
			class := config.ClassifyReload(segments)
			if reloadFlag != "" {
				if override, ok := config.ParseReloadClass(reloadFlag); ok {
					class = override
				} else {
					return fmt.Errorf("--reload must be one of: next-rpc, hup, restart")
				}
			}
			switch {
			case dryRun:
				fmt.Printf("dry-run: would set %s (%s)\n", res.Path, class.Hint(res.Path))
			case !res.Wrote:
				fmt.Printf("set %s — no change (value already matches; overlay file untouched)\n", res.Path)
			default:
				fmt.Printf("set %s — %s\n", res.Path, class.Hint(res.Path))
			}
			for _, pp := range res.PrunedPaths {
				fmt.Printf("pruned %s (inactive for new gateway.auth.mode)\n", pp)
			}
			for _, sp := range res.StaleOpenclawPaths {
				fmt.Fprintf(os.Stderr, "warn: %s still set in the openclaw layer; merged view will keep that value until you remove it from ~/.openclaw/openclaw.json\n", sp)
			}
			return nil
		},
	}
	setCmd.Flags().BoolVar(&strictJSON, "strict-json", false, "require <value> to be valid JSON (no raw-string fallback)")
	setCmd.Flags().BoolVar(&legacyJSON, "json", false, "legacy alias for --strict-json")
	setCmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate the change without writing the config file")
	setCmd.Flags().BoolVar(&mergeFlag, "merge", false, "deep-merge object/map values instead of replacing")
	setCmd.Flags().BoolVar(&replaceAll, "replace", false, "allow full replacement of protected map/list paths")
	setCmd.Flags().StringVar(&reloadFlag, "reload", "", "override the reload class for the post-write hint (next-rpc|hup|restart)")
	c.AddCommand(setCmd)

	c.AddCommand(&cobra.Command{
		Use:   "unset <path>",
		Short: "Remove a value from the talon overlay (does not modify openclaw)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			segments, err := config.ParsePath(args[0])
			if err != nil {
				return err
			}
			if err := config.Unset(resolvePaths(), segments); err != nil {
				if errors.Is(err, config.ErrNotInOverlay) {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					return err
				}
				return err
			}
			fmt.Printf("unset %s\n", config.SegPath(segments))
			return nil
		},
	})

	var (
		validateStrict     bool
		validateSyntaxOnly bool
	)
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the merged config (schema-aware when cached) and refresh the last-good sidecar",
		Long: `Validates the merged talon-over-openclaw config. By default uses the cached
JSON schema at ~/.talon/cache/config-schema.json — populate it with
"talon config schema --refresh" against a running gateway.

Without --strict, falls back to syntax-only validation when no schema cache
is available. Use --syntax-only to skip schema validation entirely.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := resolvePaths()
			// Always do the syntax-level pass first; it also refreshes
			// last-good on the talon overlay when it succeeds.
			if err := config.Validate(p); err != nil {
				return err
			}
			if validateSyntaxOnly {
				fmt.Printf("valid (syntax-only): %s\n", p.Talon.Config)
				return nil
			}
			res, err := config.ValidateMerged(p)
			if err != nil {
				var compileErr *config.ErrSchemaCompileFailed
				switch {
				case errors.Is(err, config.ErrSchemaNotCached):
					if validateStrict {
						return err
					}
					fmt.Fprintln(os.Stderr, "warn: no schema cache; falling back to syntax-only. Run 'talon config schema --refresh' against a running gateway to enable schema validation.")
					fmt.Printf("valid (syntax-only): %s\n", p.Talon.Config)
					return nil
				case errors.As(err, &compileErr):
					if validateStrict {
						return err
					}
					fmt.Fprintf(os.Stderr, "warn: cached schema is unusable (%v); falling back to syntax-only.\n", compileErr.Err)
					fmt.Printf("valid (syntax-only): %s\n", p.Talon.Config)
					return nil
				default:
					return err
				}
			}
			if !res.Valid() {
				fmt.Fprintf(os.Stderr, "invalid against schema (%s):\n", config.FormatGeneratedAt(res.SchemaGeneratedAt))
				for _, issue := range res.Issues {
					fmt.Fprintf(os.Stderr, "  %s: %s\n", issue.Path, issue.Message)
				}
				return fmt.Errorf("merged config has %d schema issue(s)", len(res.Issues))
			}
			fmt.Printf("valid: %s (schema %s)\n", p.Talon.Config, config.FormatGeneratedAt(res.SchemaGeneratedAt))
			return nil
		},
	}
	validateCmd.Flags().BoolVar(&validateStrict, "strict", false, "fail when no schema cache is available (instead of falling back to syntax-only)")
	validateCmd.Flags().BoolVar(&validateSyntaxOnly, "syntax-only", false, "skip schema validation; check JSON syntax only")
	c.AddCommand(validateCmd)

	var (
		schemaRefresh bool
		schemaSection string
	)
	schemaCmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the cached JSON schema (use --refresh to fetch from a gateway)",
		Long: `Prints the cached config schema at ~/.talon/cache/config-schema.json.

With --refresh, fetches the schema from a running gateway via the
config.schema RPC, writes it to the cache, then prints it. The cache is what
"talon config validate" uses, so refresh whenever the gateway upgrades.

With --section <name>, prints just one subschema under the schema's
top-level properties tree. Names may be dotted to drill in (e.g.
--section gateway.auth walks schema.properties.gateway.properties.auth).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := resolvePaths()
			emitOut := func(raw []byte) error {
				if schemaSection == "" {
					emit(raw)
					return nil
				}
				sub, err := config.ExtractSchemaSection(raw, schemaSection)
				if err != nil {
					return err
				}
				emit(sub)
				return nil
			}
			if schemaRefresh {
				payload, err := runRPC("config.schema", nil)
				if err != nil {
					return fmt.Errorf("fetch schema from gateway: %w", err)
				}
				if err := config.WriteSchemaCache(p, payload); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "cached schema → %s\n", p.Talon.SchemaCachePath())
				return emitOut(payload)
			}
			raw, err := os.ReadFile(p.Talon.SchemaCachePath())
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("no cached schema at %s — run 'talon config schema --refresh'", p.Talon.SchemaCachePath())
				}
				return err
			}
			return emitOut(raw)
		},
	}
	schemaCmd.Flags().BoolVar(&schemaRefresh, "refresh", false, "fetch the schema from a running gateway and update the cache")
	schemaCmd.Flags().StringVar(&schemaSection, "section", "", "print only the named top-level subschema (dotted to drill in, e.g. gateway.auth)")
	c.AddCommand(schemaCmd)

	return c
}

func modelsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "models",
		Short: "Model discovery, defaults, fallbacks, and aliases",
		// Parent default mirrors openclaw: bare `models` prints the
		// list. Keeps backward compat for users who learned it as a
		// single command before subcommands landed.
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := runRPC("models.list", nil)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			return renderModels(os.Stdout, payload)
		},
	}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List available models",
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := runRPC("models.list", nil)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			return renderModels(os.Stdout, payload)
		},
	})
	c.AddCommand(modelsSetCmd())
	c.AddCommand(modelsFallbacksCmd())
	c.AddCommand(modelsAliasesCmd())
	return c
}

func agentsCmd() *cobra.Command {
	c := &cobra.Command{Use: "agents", Short: "Manage agents"}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := runRPC("agents.list", nil)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			return renderAgents(os.Stdout, payload)
		},
	})
	c.AddCommand(agentsBindingsCmd())
	return c
}
