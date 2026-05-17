# go-plugin Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bespoke spawn/handshake/host gRPC layer in `internal/plugin/` + `internal/pluginrun/` with `github.com/hashicorp/go-plugin` for the six first-party Go plugins (deepseek, telegram, brave, whisper, bluebubbles, mac-notify).

**Architecture:** Bifurcate `internal/plugin/`. Existing code moves to `internal/plugin/legacy/` (keeps serving the Node shim path for openclaw-bundled extensions). New `internal/plugin/native/` wraps `hashicorp/go-plugin` with AutoMTLS and uses `GRPCBroker` for the bidi `Plugin` ↔ `Host` services. `pb/` (protobuf) stays untouched. `internal/pluginrun/` and `apps/talon-*-plugin/` delete entirely — `talon plugin run <name>` is the only entry point.

**Tech Stack:** Go 1.22+, google.golang.org/grpc, github.com/hashicorp/go-plugin, github.com/hashicorp/go-hclog (transitive), existing protobuf in `internal/plugin/pb/`.

**Tracking issue:** talon-e4h.

---

## File Map

**New files:**
- `internal/plugin/native/serve.go` — plugin-side entry (`Serve(name, srv)` wraps `plugin.Serve`).
- `internal/plugin/native/host.go` — host-side spawn (`Host`, `LoadPlugin`, lifecycle via `plugin.Client`).
- `internal/plugin/native/grpcplugin.go` — implements `plugin.GRPCPlugin` for the talon `Plugin` service; stashes the broker on both ends.
- `internal/plugin/native/hostsvc.go` — per-plugin `pb.HostServer` implementation, identity bound at `broker.AcceptAndServe` time.
- `internal/plugin/native/hclog_slog.go` — adapter from go-plugin's hclog interface to talon's slog logger.
- `internal/plugin/native/testplugin/main.go` — test fixture binary.
- `internal/plugin/native/host_test.go` — end-to-end bidi smoke test against testplugin.
- `internal/plugin/pkgutil/resolve.go` — `ResolvePluginCmd` hoisted out of legacy (both paths need it).

**Moved (package rename `plugin` → `legacy`):**
- `internal/plugin/{spawn,handshake,host,channels,provider,tools,image_provider}.go` → `internal/plugin/legacy/`
- `internal/plugin/{spawn,host,channels,provider,tools,image_provider}_test.go` → `internal/plugin/legacy/`
- `internal/plugin/testplugin/main.go` → `internal/plugin/legacy/testplugin/main.go`

**Modified:**
- `internal/plugin/pb/plugin.proto` — add `int64 host_broker_id = 3;` to `InitializeRequest`, mark `auth_cookie`/`host_address` as deprecated. Regenerate `plugin.pb.go`/`plugin_grpc.pb.go`.
- `cmd/talon/plugin_run.go` — `pluginrun.Serve` → `native.Serve`.
- `cmd/talon/gateway.go`, `cmd/talon/gateway_chat.go`, `cmd/talon/gateway_images.go`, `cmd/talon/gateway_plugins_test.go` — update imports from `internal/plugin` to `internal/plugin/legacy` (shim path) or `internal/plugin/native` (first-party). Wire `Kind` field into `pluginSpec`.
- `internal/server/server.go`, `internal/server/chat.go`, `internal/server/plugin_deps.go` — same import split + native vs legacy host wiring.
- `go.mod`, `go.sum` — add `github.com/hashicorp/go-plugin`.
- `CLAUDE.md` — replace `internal/pluginrun/` references with `internal/plugin/native/`.

**Deleted:**
- `internal/pluginrun/serve.go` (54 LOC).
- `apps/talon-bluebubbles-plugin/`, `apps/talon-brave-plugin/`, `apps/talon-deepseek-plugin/`, `apps/talon-mac-notify-plugin/`, `apps/talon-telegram-plugin/`, `apps/talon-whisper-plugin/`.
- Any `Makefile` `plugins` target rules that build those binaries.

---

## Phase 1: Dependency + Legacy Move

### Task 1: Add hashicorp/go-plugin dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dep**

Run: `go get github.com/hashicorp/go-plugin@latest`
Expected: `go.mod` gains `github.com/hashicorp/go-plugin vX.Y.Z`, `go.sum` updates.

- [ ] **Step 2: Verify it builds**

Run: `go build ./...`
Expected: PASS (no code uses the import yet — just verifying the dep resolves).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add hashicorp/go-plugin for plugin host migration (talon-e4h)"
```

---

### Task 2: Move bespoke plugin layer to internal/plugin/legacy/

**Files:**
- Move: `internal/plugin/spawn.go`, `handshake.go`, `host.go`, `channels.go`, `provider.go`, `tools.go`, `image_provider.go` (+ corresponding `_test.go`) → `internal/plugin/legacy/`
- Move: `internal/plugin/testplugin/` → `internal/plugin/legacy/testplugin/`
- Keep in place: `internal/plugin/pb/` (untouched).

- [ ] **Step 1: Move files via git mv**

```bash
mkdir -p internal/plugin/legacy
git mv internal/plugin/spawn.go internal/plugin/legacy/
git mv internal/plugin/spawn_test.go internal/plugin/legacy/
git mv internal/plugin/handshake.go internal/plugin/legacy/
git mv internal/plugin/host.go internal/plugin/legacy/
git mv internal/plugin/host_test.go internal/plugin/legacy/
git mv internal/plugin/channels.go internal/plugin/legacy/
git mv internal/plugin/channels_test.go internal/plugin/legacy/
git mv internal/plugin/provider.go internal/plugin/legacy/
git mv internal/plugin/provider_test.go internal/plugin/legacy/
git mv internal/plugin/tools.go internal/plugin/legacy/
git mv internal/plugin/tools_test.go internal/plugin/legacy/
git mv internal/plugin/image_provider.go internal/plugin/legacy/
git mv internal/plugin/testplugin internal/plugin/legacy/testplugin
```

- [ ] **Step 2: Rename package in moved files**

Run: `find internal/plugin/legacy -name "*.go" -not -path "*/testplugin/*" -exec sed -i '' 's/^package plugin$/package legacy/' {} +`

Expected: every moved file now starts with `package legacy`. testplugin keeps `package main`.

- [ ] **Step 3: Update importers — `internal/plugin` → `internal/plugin/legacy`, alias as `plugin`**

For each importer (`cmd/talon/gateway.go`, `cmd/talon/gateway_plugins_test.go`, `cmd/talon/gateway_chat.go`, `cmd/talon/gateway_images.go`, `internal/server/server.go`, `internal/server/chat.go`, `internal/server/plugin_deps.go`), change:

```go
"github.com/guygrigsby/talon/internal/plugin"
```

to:

```go
plugin "github.com/guygrigsby/talon/internal/plugin/legacy"
```

This is a temporary alias so callers don't have to rename `plugin.Host`, `plugin.NewHost`, etc. — those identifiers will be split across `legacy.` and `native.` in Phase 4. Aliasing now keeps each phase compilable.

- [ ] **Step 4: Verify build + tests**

Run: `go build ./...`
Expected: PASS.

Run: `go test ./internal/plugin/legacy/... ./cmd/talon/... ./internal/server/...`
Expected: All tests pass — this is a pure move + alias, behavior unchanged.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "plugins: move bespoke spawn/host/handshake to internal/plugin/legacy (talon-e4h)"
```

---

### Task 3: Hoist resolvePluginCmd into internal/plugin/pkgutil/

**Files:**
- Create: `internal/plugin/pkgutil/resolve.go`
- Create: `internal/plugin/pkgutil/resolve_test.go`
- Modify: `internal/plugin/legacy/spawn.go` (delete `resolvePluginCmd`, call `pkgutil.ResolvePluginCmd` instead).
- Move tests: any `resolvePluginCmd`-specific test cases from `legacy/spawn_test.go` → `pkgutil/resolve_test.go`.

- [ ] **Step 1: Create pkgutil/resolve.go**

Copy `resolvePluginCmd` from `internal/plugin/legacy/spawn.go` (currently at line 163-197) verbatim into a new file as exported `ResolvePluginCmd`:

```go
// Package pkgutil holds helpers shared between the legacy and native
// plugin hosts. Both need to resolve a configured plugin cmd against
// the filesystem (with sibling-of-talon + PATH fallbacks); this is the
// one place that logic lives.
package pkgutil

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// ResolvePluginCmd locates the plugin binary referenced by cmd[0],
// falling back to sibling-of-talon then $PATH when the configured path
// isn't on disk. Returns the resolved cmd (cmd[0] possibly rewritten,
// cmd[1:] unchanged) or an error naming every location searched.
func ResolvePluginCmd(name string, cmd []string) ([]string, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("plugin %s: empty Cmd", name)
	}
	bin := cmd[0]
	if _, err := os.Stat(bin); err == nil {
		return cmd, nil
	}
	base := filepath.Base(bin)
	tried := []string{bin}
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), base)
		if sibling != bin {
			if _, err := os.Stat(sibling); err == nil {
				slog.Info("plugin cmd resolved via sibling",
					"plugin", name, "configured", bin, "resolved", sibling)
				out := append([]string{sibling}, cmd[1:]...)
				return out, nil
			}
			tried = append(tried, sibling)
		}
	}
	if found, err := exec.LookPath(base); err == nil && found != bin {
		slog.Info("plugin cmd resolved via PATH",
			"plugin", name, "configured", bin, "resolved", found)
		out := append([]string{found}, cmd[1:]...)
		return out, nil
	}
	tried = append(tried, "$PATH/"+base)
	return nil, fmt.Errorf("plugin %s: cmd not found (tried %v)", name, tried)
}
```

- [ ] **Step 2: Replace caller in legacy/spawn.go**

Delete `resolvePluginCmd` function (lines 163-197). Change the call site from `resolvePluginCmd(name, opts.Cmd)` to `pkgutil.ResolvePluginCmd(name, opts.Cmd)`. Add import `"github.com/guygrigsby/talon/internal/plugin/pkgutil"`.

- [ ] **Step 3: Move resolve-specific tests**

Identify test functions in `internal/plugin/legacy/spawn_test.go` that test `resolvePluginCmd` (search for `resolvePluginCmd` references). Move those test functions into `internal/plugin/pkgutil/resolve_test.go` with package `pkgutil_test`. Rename calls from `resolvePluginCmd` to `pkgutil.ResolvePluginCmd`.

- [ ] **Step 4: Verify**

Run: `go test ./internal/plugin/...`
Expected: PASS, no functional change.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "plugins: hoist ResolvePluginCmd to internal/plugin/pkgutil for native+legacy share (talon-e4h)"
```

---

## Phase 2: Native Package Scaffold

### Task 4: Scaffold internal/plugin/native/ with empty types that compile

**Files:**
- Create: `internal/plugin/native/serve.go`
- Create: `internal/plugin/native/host.go`
- Create: `internal/plugin/native/grpcplugin.go`
- Create: `internal/plugin/native/hostsvc.go`
- Create: `internal/plugin/native/hclog_slog.go`

- [ ] **Step 1: Create grpcplugin.go skeleton**

```go
// Package native is the hashicorp/go-plugin-based host and serve
// implementation for first-party Go plugins. The legacy package
// (internal/plugin/legacy) continues to handle the Node shim path
// for openclaw-bundled extensions.
package native

import (
	"context"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// Handshake is the magic-cookie pair go-plugin uses to refuse
// bare-shell invocations of plugin binaries. The value matches the
// legacy package's TALON_PLUGIN_HANDSHAKE constant so we can keep
// the same env name across both transports.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "TALON_PLUGIN_HANDSHAKE",
	MagicCookieValue: "talon.plugin.v1",
}

// PluginMapKey is the registry key both sides use for talon's Plugin
// service. go-plugin allows multiple plugin types per binary; we only
// have one.
const PluginMapKey = "talon-plugin"

// grpcPlugin implements goplugin.GRPCPlugin. The plugin-side instance
// holds an Impl (pb.PluginServer) it registers in GRPCServer; the
// host-side instance produces a pb.PluginClient in GRPCClient. Both
// sides capture the GRPCBroker for the bidi Host-service channel.
type grpcPlugin struct {
	goplugin.Plugin
	Impl pb.PluginServer // set on plugin side, nil on host side

	// broker is set by go-plugin when GRPCServer / GRPCClient runs.
	// Plugin side reads it from a stashed location during Initialize
	// to dial back to the host's HostServer. Host side reads it to
	// AcceptAndServe a per-plugin HostServer instance.
	broker *goplugin.GRPCBroker
}

func (p *grpcPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	p.broker = broker
	pb.RegisterPluginServer(s, p.Impl)
	return nil
}

func (p *grpcPlugin) GRPCClient(_ context.Context, broker *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	p.broker = broker
	return pb.NewPluginClient(c), nil
}
```

- [ ] **Step 2: Create serve.go skeleton**

```go
package native

import (
	"log/slog"
	"os"

	goplugin "github.com/hashicorp/go-plugin"

	talonlog "github.com/guygrigsby/talon/internal/log"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// pluginImpl is what the plugin-side Serve hands go-plugin. It
// embeds the user's pb.PluginServer and gives Initialize access to
// the broker (stashed by grpcPlugin.GRPCServer) so it can register
// itself for host callbacks. Implementations that need a host client
// should embed *HostClientHolder (see TODO below in Task 7).
type pluginImpl struct {
	pb.PluginServer
}

// Serve is the plugin-side entry point. Each first-party plugin's
// main calls native.Serve("<name>", impl) and never returns. The
// host has set the TALON_PLUGIN_HANDSHAKE env var via go-plugin's
// magic cookie; if missing, go-plugin prints the handshake-help
// message and exits non-zero.
func Serve(name string, srv pb.PluginServer) {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	logger := slog.With("plugin", name)

	gp := &grpcPlugin{Impl: srv}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{
			1: {PluginMapKey: gp},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
		Logger:     newHCLogAdapter(logger),
	})
}
```

- [ ] **Step 3: Create host.go skeleton**

```go
package native

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	goplugin "github.com/hashicorp/go-plugin"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/plugin/pkgutil"
)

// Instance is a registered, running first-party plugin. Mirrors
// legacy.Instance's surface so callers can be migrated incrementally.
type Instance struct {
	Name     string
	Manifest *pb.Manifest
	Client   pb.PluginClient

	client *goplugin.Client // go-plugin handle for lifecycle
	stop   func()
}

func (i *Instance) Stop() {
	if i == nil || i.stop == nil {
		return
	}
	i.stop()
	i.stop = nil
}

// Host is the registry for native plugins. One per gateway.
type Host struct {
	mu     sync.RWMutex
	byName map[string]*Instance

	// hostSvcFactory builds a per-plugin pb.HostServer at broker.AcceptAndServe
	// time. The factory closes over whatever the gateway needs to satisfy
	// GetConfig/RunSubagent/etc.
	hostSvcFactory func(pluginName string) pb.HostServer
}

func NewHost(hostSvcFactory func(pluginName string) pb.HostServer) *Host {
	return &Host{
		byName:         make(map[string]*Instance),
		hostSvcFactory: hostSvcFactory,
	}
}

// LoadOptions is the spawn config. Cmd is the binary + args
// (BuiltinPluginCmd output or a third-party override). Env extends the
// host environment; go-plugin appends the magic cookie automatically.
type LoadOptions struct {
	Cmd []string
	Env []string
}

// LoadPlugin spawns name's process via go-plugin, waits for the
// handshake + mTLS exchange, registers the plugin, and runs the
// Initialize RPC.
func (h *Host) LoadPlugin(ctx context.Context, name string, opts LoadOptions) (*Instance, error) {
	// Implementation in Task 6.
	return nil, fmt.Errorf("not implemented")
}

// Unregister removes name from the registry. Called both by user
// Stop() and by the lifecycle goroutine on plugin.Client.Exited().
func (h *Host) Unregister(name string) {
	h.mu.Lock()
	inst := h.byName[name]
	delete(h.byName, name)
	h.mu.Unlock()
	if inst != nil {
		inst.Stop()
	}
}

// Get returns the registered instance for name, or nil.
func (h *Host) Get(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byName[name]
}

// silence unused-import noise until Task 6 fills LoadPlugin
var _ = exec.Command
var _ = pkgutil.ResolvePluginCmd
```

- [ ] **Step 4: Create hostsvc.go skeleton**

```go
package native

import (
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// HostSvcDeps is the surface the gateway provides to power the
// pb.HostServer methods. Each first-party Host method needs at most
// a handful of these; we accept one struct so the field set can grow
// without rewriting every constructor call.
type HostSvcDeps struct {
	// Populated in Task 7 — config reader, agents lister, subagent runner, etc.
}

// hostServer is one pb.HostServer instance bound to a specific
// plugin. The plugin name is captured at broker.AcceptAndServe time
// so each method already knows which plugin's capabilities to check.
type hostServer struct {
	pb.UnimplementedHostServer
	plugin string
	deps   HostSvcDeps
}

func newHostServer(pluginName string, deps HostSvcDeps) pb.HostServer {
	return &hostServer{plugin: pluginName, deps: deps}
}
```

- [ ] **Step 5: Create hclog_slog.go**

```go
package native

import (
	"io"
	"log"
	"log/slog"

	"github.com/hashicorp/go-hclog"
)

// newHCLogAdapter routes go-plugin's hclog output through talon's
// slog logger. Without this, go-plugin emits its own JSON to stderr,
// which fights with talon's slog handler.
func newHCLogAdapter(s *slog.Logger) hclog.Logger {
	return &hclogToSlog{s: s}
}

type hclogToSlog struct {
	s    *slog.Logger
	name string
}

func (h *hclogToSlog) log(level slog.Level, msg string, args ...any) {
	h.s.Log(nil, level, msg, args...)
}

func (h *hclogToSlog) Trace(msg string, args ...any) { h.log(slog.LevelDebug, msg, args...) }
func (h *hclogToSlog) Debug(msg string, args ...any) { h.log(slog.LevelDebug, msg, args...) }
func (h *hclogToSlog) Info(msg string, args ...any)  { h.log(slog.LevelInfo, msg, args...) }
func (h *hclogToSlog) Warn(msg string, args ...any)  { h.log(slog.LevelWarn, msg, args...) }
func (h *hclogToSlog) Error(msg string, args ...any) { h.log(slog.LevelError, msg, args...) }

func (h *hclogToSlog) IsTrace() bool { return h.s.Enabled(nil, slog.LevelDebug) }
func (h *hclogToSlog) IsDebug() bool { return h.s.Enabled(nil, slog.LevelDebug) }
func (h *hclogToSlog) IsInfo() bool  { return h.s.Enabled(nil, slog.LevelInfo) }
func (h *hclogToSlog) IsWarn() bool  { return h.s.Enabled(nil, slog.LevelWarn) }
func (h *hclogToSlog) IsError() bool { return h.s.Enabled(nil, slog.LevelError) }

func (h *hclogToSlog) ImpliedArgs() []any           { return nil }
func (h *hclogToSlog) With(args ...any) hclog.Logger { return &hclogToSlog{s: h.s.With(args...), name: h.name} }
func (h *hclogToSlog) Name() string                  { return h.name }
func (h *hclogToSlog) Named(name string) hclog.Logger {
	if h.name != "" {
		name = h.name + "." + name
	}
	return &hclogToSlog{s: h.s.With("named", name), name: name}
}
func (h *hclogToSlog) ResetNamed(name string) hclog.Logger { return &hclogToSlog{s: h.s, name: name} }
func (h *hclogToSlog) SetLevel(hclog.Level)                {}
func (h *hclogToSlog) GetLevel() hclog.Level               { return hclog.Info }

// StandardLogger/StandardWriter: go-plugin asks for these for
// stdlib-style sinks. Route to a discarding writer; we don't care
// about the stdlib log integration.
func (h *hclogToSlog) StandardLogger(*hclog.StandardLoggerOptions) *log.Logger {
	return log.New(io.Discard, "", 0)
}
func (h *hclogToSlog) StandardWriter(*hclog.StandardLoggerOptions) io.Writer {
	return io.Discard
}
```

- [ ] **Step 6: Verify scaffold compiles**

Run: `go build ./internal/plugin/native/...`
Expected: PASS — package compiles, every type exists, no behavior yet.

- [ ] **Step 7: Commit**

```bash
git add internal/plugin/native
git commit -m "plugins/native: scaffold go-plugin host+serve packages (talon-e4h)"
```

---

## Phase 3: Plugin-Side Serve

### Task 5: Wire native.Serve into talon plugin run + delete pluginrun

**Files:**
- Modify: `cmd/talon/plugin_run.go`
- Delete: `internal/pluginrun/serve.go`

- [ ] **Step 1: Update plugin_run.go**

Read: `cmd/talon/plugin_run.go`. Change import from `"github.com/guygrigsby/talon/internal/pluginrun"` to `"github.com/guygrigsby/talon/internal/plugin/native"`. Change the body of `RunE`:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    name := args[0]
    ctor, ok := pluginConstructors[name]
    if !ok {
        return fmt.Errorf("unknown plugin %q (known: %v)", name, pluginNames())
    }
    srv, err := ctor()
    if err != nil {
        return fmt.Errorf("plugin %s: init failed: %w", name, err)
    }
    native.Serve(name, srv)
    return nil // unreachable; Serve never returns
},
```

- [ ] **Step 2: Delete pluginrun**

Run: `git rm internal/pluginrun/serve.go && rmdir internal/pluginrun 2>/dev/null; true`

- [ ] **Step 3: Build everything (apps/ still imports pluginrun — expect failure)**

Run: `go build ./...`
Expected: FAIL — `apps/talon-*-plugin/main.go` imports the deleted package. This is intentional; the apps delete in Task 5b.

- [ ] **Step 4: Delete the app wrappers**

Run:
```bash
git rm -r apps/talon-bluebubbles-plugin apps/talon-brave-plugin apps/talon-deepseek-plugin apps/talon-mac-notify-plugin apps/talon-telegram-plugin apps/talon-whisper-plugin
```

Check `Makefile` for any rule referencing `apps/talon-*-plugin`:

Run: `grep -n "talon-.*-plugin" Makefile`

If the `plugins` target builds these binaries (it does — see `make plugins` rule), simplify the target so it just builds `bin/talon` (or delete the target entirely if it has no other reason to exist). Same for any CI references.

- [ ] **Step 5: Build clean**

Run: `go build ./...`
Expected: PASS.

Run: `go vet ./...`
Expected: PASS.

- [ ] **Step 6: Manual smoke — run a plugin out-of-band**

Run: `TALON_PLUGIN_HANDSHAKE=talon.plugin.v1 ./bin/talon plugin run deepseek`
Expected: go-plugin's handshake-help message (because the magic cookie value matches but go-plugin's handshake protocol requires being launched by a parent that speaks the protocol — this is the correct refusal).

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "plugins: delete pluginrun and apps/talon-*-plugin/, route through native.Serve (talon-e4h)"
```

---

## Phase 4: Host-Side Spawn (Plugin → Host RPCs Work)

### Task 6: Implement native.Host.LoadPlugin

**Files:**
- Modify: `internal/plugin/native/host.go`

- [ ] **Step 1: Implement LoadPlugin**

Replace the stub LoadPlugin in `internal/plugin/native/host.go`:

```go
func (h *Host) LoadPlugin(ctx context.Context, name string, opts LoadOptions) (*Instance, error) {
	if len(opts.Cmd) == 0 {
		return nil, fmt.Errorf("plugin %s: empty Cmd", name)
	}

	resolved, err := pkgutil.ResolvePluginCmd(name, opts.Cmd)
	if err != nil {
		return nil, err
	}

	gp := &grpcPlugin{} // host side — no Impl; populated by GRPCClient

	cmd := exec.Command(resolved[0], resolved[1:]...)
	cmd.Env = append(cmd.Env, opts.Env...)

	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{
			1: {PluginMapKey: gp},
		},
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		AutoMTLS:         true,
		Logger:           newHCLogAdapter(slog.With("plugin", name)),
	})

	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin %s: client: %w", name, err)
	}
	raw, err := rpc.Dispense(PluginMapKey)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin %s: dispense: %w", name, err)
	}
	pluginClient, ok := raw.(pb.PluginClient)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("plugin %s: dispense returned %T, want pb.PluginClient", name, raw)
	}

	// Stand up the per-plugin Host service on the broker BEFORE
	// Initialize, so the plugin can immediately call back during init.
	brokerID := gp.broker.NextId()
	hostSvc := h.hostSvcFactory(name)
	go gp.broker.AcceptAndServe(brokerID, func(opts []grpc.ServerOption) *grpc.Server {
		s := grpc.NewServer(opts...)
		pb.RegisterHostServer(s, hostSvc)
		return s
	})

	resp, err := pluginClient.Initialize(ctx, &pb.InitializeRequest{
		HostBrokerId: int64(brokerID),
	})
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin %s: initialize: %w", name, err)
	}

	inst := &Instance{
		Name:     name,
		Manifest: resp.GetManifest(),
		Client:   pluginClient,
		client:   client,
		stop: func() {
			// Best-effort Shutdown then kill.
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutCancel()
			_, _ = pluginClient.Shutdown(shutCtx, &pb.ShutdownRequest{})
			client.Kill()
		},
	}

	h.mu.Lock()
	if _, exists := h.byName[name]; exists {
		h.mu.Unlock()
		inst.Stop()
		return nil, fmt.Errorf("plugin %s: already registered", name)
	}
	h.byName[name] = inst
	h.mu.Unlock()

	// Lifecycle: when plugin.Client signals Exited, unregister.
	go func() {
		<-client.Exited()
		h.Unregister(name)
	}()

	return inst, nil
}
```

Add imports as needed: `log/slog`, `time`, `google.golang.org/grpc`.

- [ ] **Step 2: Add HostBrokerId to proto + regenerate**

Read: `internal/plugin/pb/plugin.proto`. Locate `message InitializeRequest`. Edit:

```proto
message InitializeRequest {
  // Auth cookie negotiated during the subprocess handshake. DEPRECATED
  // — populated only on the legacy (Node shim) path. The native go-plugin
  // host uses connection identity via GRPCBroker instead.
  string auth_cookie = 1 [deprecated = true];
  // Address (host:port) of the host's Host-service. DEPRECATED — see
  // above. Native plugins dial back via host_broker_id.
  string host_address = 2 [deprecated = true];
  // GRPCBroker id the plugin uses to dial back to the host's Host
  // service over the existing gRPC connection. Populated by native
  // hosts (talon-e4h); zero on the legacy path.
  int64 host_broker_id = 3;
}
```

Regenerate. Check the existing generator pattern:

Run: `grep -rn "protoc\|buf generate\|go generate" internal/plugin/pb/ Makefile`

If there's a `make proto` or similar, run it. Otherwise use:

Run: `protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative internal/plugin/pb/plugin.proto`

Expected: `internal/plugin/pb/plugin.pb.go` regenerates with `HostBrokerId` field on `InitializeRequest`.

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: PASS (legacy code still uses `auth_cookie`/`host_address` — deprecation is a doc-only signal).

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "plugins/native: implement Host.LoadPlugin with GRPCBroker handoff (talon-e4h)"
```

---

### Task 7: Update each first-party plugin to dial back via the broker

**Files:**
- Modify: `internal/plugins/deepseek/plugin.go`, `internal/plugins/telegram/plugin.go`, `internal/plugins/brave/plugin.go`, `internal/plugins/whisper/plugin.go`, `internal/plugins/bluebubbles/plugin.go`, `internal/plugins/macnotify/plugin.go`

Each plugin's `Initialize` handler currently reads `req.AuthCookie` + `req.HostAddress` and dials the host listener directly. After this task they use the broker.

- [ ] **Step 1: Add a HostClientHolder helper in native/serve.go**

Append to `internal/plugin/native/serve.go`:

```go
// HostClientHolder is a goroutine-safe slot for the pb.HostClient a
// plugin uses to call back into the host. Plugins embed *HostClientHolder
// into their own pb.PluginServer, then call SetFromBroker inside
// Initialize once the host has told them the broker id.
//
// The holder needs the GRPCBroker the plugin captured at GRPCServer
// time. Plugins get that broker via the package-level CurrentBroker()
// accessor, which native.Serve sets before plugin.Serve returns.
type HostClientHolder struct {
	mu sync.Mutex
	c  pb.HostClient
}

func (h *HostClientHolder) Get() pb.HostClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.c
}

// SetFromBroker dials the broker id the host sent in InitializeRequest
// and caches the resulting pb.HostClient. Safe to call from a plugin's
// Initialize handler.
func (h *HostClientHolder) SetFromBroker(brokerID int64) error {
	b := currentBroker()
	if b == nil {
		return fmt.Errorf("native: no broker captured (Serve not called?)")
	}
	conn, err := b.Dial(uint32(brokerID))
	if err != nil {
		return fmt.Errorf("native: broker dial %d: %w", brokerID, err)
	}
	h.mu.Lock()
	h.c = pb.NewHostClient(conn)
	h.mu.Unlock()
	return nil
}

// currentBroker / setCurrentBroker bridge the plugin-side broker
// captured in grpcPlugin.GRPCServer to plugins that don't see the
// broker directly (Serve abstracts it away). Set once, before
// goplugin.Serve returns.
var (
	currentBrokerMu sync.Mutex
	curBroker       *goplugin.GRPCBroker
)

func setCurrentBroker(b *goplugin.GRPCBroker) {
	currentBrokerMu.Lock()
	defer currentBrokerMu.Unlock()
	curBroker = b
}

func currentBroker() *goplugin.GRPCBroker {
	currentBrokerMu.Lock()
	defer currentBrokerMu.Unlock()
	return curBroker
}
```

Update `grpcPlugin.GRPCServer` in `grpcplugin.go` to also call `setCurrentBroker(broker)`:

```go
func (p *grpcPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	p.broker = broker
	setCurrentBroker(broker)
	pb.RegisterPluginServer(s, p.Impl)
	return nil
}
```

Add imports: `sync`, `fmt` to serve.go.

- [ ] **Step 2: For each plugin, embed HostClientHolder and use it in Initialize**

Example for deepseek (`internal/plugins/deepseek/plugin.go`):

```go
type Plugin struct {
	pb.UnimplementedPluginServer
	*native.HostClientHolder // new
	// existing fields...
}

func New() (pb.PluginServer, error) {
	return &Plugin{HostClientHolder: &native.HostClientHolder{}}, nil
}

func (p *Plugin) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	if req.HostBrokerId != 0 {
		if err := p.HostClientHolder.SetFromBroker(req.HostBrokerId); err != nil {
			return nil, err
		}
	}
	// existing manifest construction...
	return &pb.InitializeResponse{Manifest: manifest}, nil
}
```

Anywhere the plugin used to dial the host directly (look for `grpc.NewClient(req.HostAddress`) or use `req.AuthCookie` in metadata), replace with `p.HostClientHolder.Get().GetConfig(...)` etc.

Repeat for telegram, brave, whisper, bluebubbles, macnotify.

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 4: Run existing plugin unit tests**

Run: `go test ./internal/plugins/...`
Expected: PASS — these tests exercise plugin internals, not transport. If any break, the test is asserting against the old direct-dial scheme; update to use a stub broker or mock the HostClient.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "plugins: dial host via GRPCBroker, not separate address (talon-e4h)"
```

---

## Phase 5: Wire Gateway Spec Dispatch

### Task 8: Add Kind field to pluginSpec and split native vs legacy dispatch

**Files:**
- Modify: `cmd/talon/gateway_chat.go` (lines ~180-220, `collectPluginSpecs`)
- Modify: `cmd/talon/gateway.go` (constructs the legacy `plugin.Host`; add a `native.Host` alongside)
- Modify: `internal/server/server.go` (passes host into the chat handler; add native host)

- [ ] **Step 1: Update gateway_chat.go**

Add `kind` to the `pluginSpec` struct:

```go
type pluginSpec struct {
	name string
	cmd  []string
	env  []string
	kind specKind
}

type specKind int

const (
	kindLegacy specKind = iota // openclaw Node shim or third-party explicit cmd
	kindNative                  // first-party Go via go-plugin
)
```

In the `ForEach` body, set `kind`:

```go
if cmd := stringArray(entry.Get("cmd")); len(cmd) > 0 {
    specs = append(specs, pluginSpec{name: nameKey.Str, cmd: cmd, env: env, kind: kindLegacy})
    return true
}
if extName := entry.Get("bundled").Str; extName != "" {
    // ... existing shim path, kind: kindLegacy
}
if cmd := server.BuiltinPluginCmd(nameKey.Str); len(cmd) > 0 {
    specs = append(specs, pluginSpec{name: nameKey.Str, cmd: cmd, env: env, kind: kindNative})
    return true
}
```

- [ ] **Step 2: Update gateway.go to construct both hosts**

Locate the line that constructs `plugin.NewHost(...)`. Add:

```go
legacyHost := plugin.NewHost(hostAddr) // unchanged — alias is internal/plugin/legacy
nativeHost := native.NewHost(makeHostSvc(server)) // makeHostSvc returns func(pluginName string) pb.HostServer
```

`makeHostSvc` is a closure over the gateway state that the existing legacy `Host`'s gRPC service implementation closed over. Extract the implementation of GetConfig/ListAgents/etc. from `internal/plugin/legacy/host.go` into a shared helper that both `legacyHostImpl` and `native.hostServer` can call. (See Task 9.)

- [ ] **Step 3: Update plugin loading code to dispatch by Kind**

Find the loop that calls `legacyHost.LoadPlugin(...)`. Split:

```go
for _, spec := range specs {
    switch spec.kind {
    case kindNative:
        if _, err := nativeHost.LoadPlugin(ctx, spec.name, native.LoadOptions{Cmd: spec.cmd, Env: spec.env}); err != nil {
            slog.Error("plugin load failed", "plugin", spec.name, "kind", "native", "err", err)
        }
    case kindLegacy:
        if _, err := legacyHost.LoadPlugin(ctx, spec.name, plugin.LoadOptions{Cmd: spec.cmd, Env: spec.env}); err != nil {
            slog.Error("plugin load failed", "plugin", spec.name, "kind", "legacy", "err", err)
        }
    }
}
```

- [ ] **Step 4: Update chat.go / gateway_chat.go to look up plugins in both hosts**

For each call to `legacyHost.Get(name)`, fall back to `nativeHost.Get(name)`. A small helper:

```go
func getPlugin(name string) *commonPluginView {
    if i := nativeHost.Get(name); i != nil {
        return &commonPluginView{Client: i.Client, Manifest: i.Manifest}
    }
    if i := legacyHost.Get(name); i != nil {
        return &commonPluginView{Client: i.Client, Manifest: i.Manifest}
    }
    return nil
}
```

Both `*legacy.Instance` and `*native.Instance` expose `Client pb.PluginClient` and `Manifest *pb.Manifest` so the view is trivial.

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 6: Update gateway_plugins_test.go**

Read: `cmd/talon/gateway_plugins_test.go`. Find the assertions about spec contents. Add an assertion that first-party entries (e.g., `deepseek`) come back with `kind: kindNative`, and third-party entries (use a fixture with explicit `cmd`) come back with `kind: kindLegacy`.

- [ ] **Step 7: Test**

Run: `go test ./cmd/talon/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "gateway: dispatch first-party plugins to native host, openclaw shim to legacy (talon-e4h)"
```

---

### Task 9: Port capability-gated Host service methods into native/hostsvc.go

**Files:**
- Modify: `internal/plugin/native/hostsvc.go`
- Reference: `internal/plugin/legacy/host.go` and any Host service handler files

- [ ] **Step 1: Identify the Host service handlers in legacy**

Run: `grep -n "func.*GetConfig\|func.*ListAgents\|func.*GetAgentIdentity\|func.*ListModels\|func.*ListSessions\|func.*GetChatHistory\|func.*AppendMemory\|func.*RunSubagent" internal/plugin/legacy/`

Expected: locate each Host-service method implementation. Note their dependencies (config reader, session store, subagent runner, etc.) — these become fields on `HostSvcDeps`.

- [ ] **Step 2: Populate HostSvcDeps**

Expand `HostSvcDeps` in `hostsvc.go` to carry the interfaces each method needs. Keep them narrow — one interface per dependency, not the whole gateway. Example:

```go
type HostSvcDeps struct {
	ConfigReader   ConfigReader
	AgentLister    AgentLister
	SubagentRunner SubagentRunner
	// ... one field per capability
}

type ConfigReader interface {
	GetForPlugin(plugin, path string) ([]byte, error)
}

// ... etc.
```

- [ ] **Step 3: Implement each handler**

For each method, port the body from legacy:

```go
func (s *hostServer) GetConfig(ctx context.Context, req *pb.GetConfigRequest) (*pb.GetConfigResponse, error) {
    raw, err := s.deps.ConfigReader.GetForPlugin(s.plugin, req.Path)
    if err != nil {
        return nil, status.Errorf(codes.PermissionDenied, "plugin %s: %v", s.plugin, err)
    }
    return &pb.GetConfigResponse{RawJson: raw}, nil
}
```

Capability gating: each method already knows `s.plugin`. The deps interfaces apply capability checks; the previous cookie-based identity step is gone.

- [ ] **Step 4: Wire the factory in cmd/talon/gateway.go**

```go
makeHostSvc := func(pluginName string) pb.HostServer {
    return native.NewHostServer(pluginName, native.HostSvcDeps{
        ConfigReader: configAdapter,
        // ...
    })
}
nativeHost := native.NewHost(makeHostSvc)
```

Add `NewHostServer` as the exported entry in `hostsvc.go`:

```go
func NewHostServer(pluginName string, deps HostSvcDeps) pb.HostServer {
    return newHostServer(pluginName, deps)
}
```

- [ ] **Step 5: Build + test**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "plugins/native: implement pb.HostServer with per-plugin identity (talon-e4h)"
```

---

## Phase 6: Integration Test + Verification

### Task 10: Add end-to-end bidi smoke test

**Files:**
- Create: `internal/plugin/native/testplugin/main.go`
- Create: `internal/plugin/native/host_test.go`

- [ ] **Step 1: Write the test fixture plugin**

```go
// Package main is the native-host integration test fixture. Echoes
// RunTool input back as output; calls GetConfig during Initialize to
// exercise the broker round-trip.
package main

import (
	"context"
	"fmt"

	"github.com/guygrigsby/talon/internal/plugin/native"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

type fixture struct {
	pb.UnimplementedPluginServer
	*native.HostClientHolder
	bootConfig []byte
}

func (f *fixture) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	if err := f.HostClientHolder.SetFromBroker(req.HostBrokerId); err != nil {
		return nil, err
	}
	resp, err := f.HostClientHolder.Get().GetConfig(ctx, &pb.GetConfigRequest{Path: "testplugin"})
	if err != nil {
		return nil, fmt.Errorf("testplugin: GetConfig failed: %w", err)
	}
	f.bootConfig = resp.GetRawJson()
	return &pb.InitializeResponse{Manifest: &pb.Manifest{Name: "testplugin", Version: "0.1.0"}}, nil
}

func (f *fixture) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	return &pb.RunToolResponse{Output: "echo:" + req.GetArgumentsJson() + "|bootConfig=" + string(f.bootConfig)}, nil
}

func (f *fixture) Shutdown(context.Context, *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	return &pb.ShutdownResponse{}, nil
}

func main() {
	native.Serve("testplugin", &fixture{HostClientHolder: &native.HostClientHolder{}})
}
```

- [ ] **Step 2: Write the host test**

```go
package native_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/plugin/native"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

type stubConfigReader struct{}

func (stubConfigReader) GetForPlugin(plugin, path string) ([]byte, error) {
	return []byte(`{"plugin":"` + plugin + `","path":"` + path + `"}`), nil
}

func TestNativeHostRoundTrip(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "testplugin")
	out, err := exec.Command("go", "build", "-o", binPath, "./testplugin").CombinedOutput()
	if err != nil {
		t.Fatalf("build testplugin: %v\n%s", err, out)
	}

	host := native.NewHost(func(pluginName string) pb.HostServer {
		return native.NewHostServer(pluginName, native.HostSvcDeps{
			ConfigReader: stubConfigReader{},
		})
	})
	t.Cleanup(func() { host.Unregister("testplugin") })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	inst, err := host.LoadPlugin(ctx, "testplugin", native.LoadOptions{Cmd: []string{binPath}})
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if inst.Manifest.Name != "testplugin" {
		t.Fatalf("manifest.name = %q, want testplugin", inst.Manifest.Name)
	}

	resp, err := inst.Client.RunTool(ctx, &pb.RunToolRequest{ToolName: "echo", ArgumentsJson: `{"x":1}`})
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	want := `echo:{"x":1}|bootConfig={"plugin":"testplugin","path":"testplugin"}`
	if resp.Output != want {
		t.Fatalf("output = %q, want %q", resp.Output, want)
	}

	// Force exit; lifecycle goroutine should Unregister.
	inst.Stop()
	deadline := time.After(2 * time.Second)
	for host.Get("testplugin") != nil {
		select {
		case <-deadline:
			t.Fatal("Unregister did not run within 2s of Stop")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	_ = os.Remove(binPath)
}
```

- [ ] **Step 3: Run**

Run: `go test ./internal/plugin/native/... -v -run TestNativeHostRoundTrip`
Expected: PASS. Verifies handshake, mTLS, Initialize, broker dial-back, GetConfig, RunTool, Shutdown, lifecycle Unregister.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "plugins/native: end-to-end bidi smoke test against testplugin (talon-e4h)"
```

---

### Task 11: Verify all six first-party plugins spawn end-to-end

**No file changes.** Manual / scripted verification.

- [ ] **Step 1: Build the binary**

Run: `make build`
Expected: `bin/talon` rebuilt.

- [ ] **Step 2: Start the gateway**

Run: `./bin/talon gateway run &` (background) or in a separate terminal.

- [ ] **Step 3: Verify ps shows the plugins**

Run: `ps -o pid,ppid,pgid,command -p $(pgrep -f "talon plugin run")`
Expected: One row per enabled first-party plugin (per `~/.openclaw/openclaw.json` + `~/.talon/openclaw.json` merged), each with PPID equal to the gateway's PID. Commands look like `/Users/.../bin/talon plugin run brave`, etc.

- [ ] **Step 4: Exercise a tool call that round-trips through one of them**

```bash
./bin/talon chat send --agent chat "Search the web for Go 1.22 release notes"
```

Expected: Successful response, with logs showing the brave plugin handled `web_search`.

- [ ] **Step 5: Exercise a provider call**

```bash
./bin/talon chat send --agent chat --model deepseek/deepseek-chat "ping"
```

Expected: Successful response routed through the deepseek native plugin.

- [ ] **Step 6: Kill the gateway, verify plugins clean up**

Run: `kill $(pgrep -f "talon gateway run")`
Run: `pgrep -f "talon plugin run" || echo "all plugins exited"`
Expected: `all plugins exited` — go-plugin's lifecycle kills children when the parent's `client.Kill()` runs (or when the parent dies, via SIGTERM cascade go-plugin sets up).

---

## Phase 7: Cleanup + Docs

### Task 12: Update CLAUDE.md and PARITY.md

**Files:**
- Modify: `CLAUDE.md`
- Modify: `PARITY.md` (only if it references plugin transport details — most likely not)
- Modify: `docs/architecture.md` (if it covers the plugin layer)

- [ ] **Step 1: Find references**

Run: `grep -n "internal/plugin\|pluginrun\|hashicorp" CLAUDE.md PARITY.md docs/architecture.md`

For each hit, decide: rewrite to point at `internal/plugin/native` for first-party, `internal/plugin/legacy` for the shim, or leave alone if generic.

- [ ] **Step 2: Add a one-paragraph note to docs/architecture.md**

Append under the plugin section (or create one if absent): a short paragraph naming the two paths (native via go-plugin; legacy bespoke for the Node shim), the GRPCBroker bidi pattern, and the AutoMTLS expectation.

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "docs: update plugin layer notes for go-plugin migration (talon-e4h)"
```

---

### Task 13: Close the beads issue

- [ ] **Step 1: Verify all acceptance criteria**

Re-read the acceptance criteria on `bd show talon-e4h`. Tick through:

- `github.com/hashicorp/go-plugin` in `go.mod` ✓
- `internal/plugin/native/` replaces `internal/pluginrun/` ✓
- Six first-party plugins spawn with AutoMTLS (Task 11 verified) ✓
- Plugin → Host RPCs work via GRPCBroker (Task 10 smoke test verified) ✓
- `apps/talon-*-plugin/` and `internal/pluginrun/` deleted ✓
- Bespoke code in `internal/plugin/legacy/` only ✓
- `internal/plugins/*/plugin_test.go` pass without modification — verify ✓
- `go test ./...` green ✓
- CLAUDE.md updated ✓

- [ ] **Step 2: File the follow-up issues**

```bash
bd create "Plugins: migrate openclaw Node shim to go-plugin (delete internal/plugin/legacy)" \
  --description "Followup to talon-e4h. The Node shim openclaw-plugin-host currently uses the bespoke spawn/handshake/host gRPC layer preserved at internal/plugin/legacy/. Port the shim to implement go-plugin's HandshakeConfig + gRPC server (in Go, replacing the Node entry binary, or via a Go bridge wrapper). Once done, delete internal/plugin/legacy/ entirely." \
  -p 2

bd create "Plugins: AutoMTLS cert pinning + protocol version negotiation policy" \
  --description "Followup to talon-e4h. AutoMTLS is on for first-party plugins but cert lifecycle and protocol version step-up policy aren't documented or enforced. Define cert rotation expectations and pick a version-negotiation policy for when we bump the wire protocol." \
  -p 3

bd create "Plugins: third-party plugin author docs for native go-plugin path" \
  --description "Followup to talon-e4h. Document how a third party writes a Go plugin against the native (go-plugin) transport: HandshakeConfig values, PluginMapKey, HostClientHolder pattern, capability declaration in the manifest." \
  -p 3
```

- [ ] **Step 3: Close talon-e4h**

```bash
bd close talon-e4h --reason "Migration complete; all six first-party plugins on go-plugin with AutoMTLS + GRPCBroker bidi. Legacy path preserved for the Node shim; tracked for follow-up. See commit log on this branch."
```

- [ ] **Step 4: Final commit**

```bash
git add -A
git commit -m "plugins: complete go-plugin migration for first-party plugins (closes talon-e4h)"
```

---

## Verification Checklist

Before declaring done:

- [ ] `go build ./...` PASS
- [ ] `go vet ./...` PASS
- [ ] `go test ./...` PASS
- [ ] `make build` produces `bin/talon`
- [ ] `./bin/talon gateway run` starts and spawns enabled first-party plugins
- [ ] `ps` shows plugins as children of gateway, running `talon plugin run <name>`
- [ ] Killing gateway terminates all plugin subprocesses
- [ ] At least one Host-callback round-trip exercised (smoke test or live chat)
- [ ] `internal/pluginrun/` and `apps/talon-*-plugin/` gone from the tree
- [ ] `internal/plugin/legacy/` exists and compiles; openclaw shim path still functional (manual test if a bundled extension is enabled)
- [ ] beads talon-e4h closed; follow-ups filed

---

## Notes for the Executor

- **Don't merge tasks.** Each task is its own commit. The migration is bisectable if anything breaks downstream.
- **Aliasing imports** (`plugin "github.com/.../internal/plugin/legacy"`) is intentional during Phase 1 — avoids a giant rename in the same commit as the move. It can be unaliased opportunistically as files are touched for other reasons.
- **AutoMTLS adds ~50ms to plugin startup.** If startup latency is a concern, the test in Task 10 measures it. Acceptable per design decision.
- **GRPCBroker connection is on the same TCP as the Plugin service** — no extra port, no extra firewall surface.
- **If protoc regeneration in Task 6 produces unexpected diff** beyond the new `host_broker_id` field, that's a stale generator version on disk; do not commit. Pin generator version first.
