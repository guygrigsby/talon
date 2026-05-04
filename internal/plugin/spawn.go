package plugin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// LoadOptions configures a plugin spawn. Cmd is the binary + args; Env
// extends os.Environ() (the host always appends its own three handshake
// vars on top, so plugin authors can't accidentally suppress them).
type LoadOptions struct {
	Cmd []string
	Env []string
	// HandshakeTimeout caps how long we'll wait for the plugin to print
	// its handshake line. Default 10s — most plugins are ready in <100ms
	// but cold-start of Node-based plugins (the openclaw shim) can take
	// longer.
	HandshakeTimeout time.Duration
}

// LoadPlugin spawns a plugin subprocess, performs the handshake, dials
// the plugin's gRPC server, and registers it via Host.Register. Returns
// the registered Instance.
//
// The subprocess inherits the host's stderr (for diagnostics) and has
// its stdout consumed: the FIRST line is parsed as the handshake; every
// subsequent line is logged as "[plugin/<name>] <line>" so plugin
// authors can debug-print without ceremony. When the subprocess exits
// the host unregisters it automatically.
func (h *Host) LoadPlugin(ctx context.Context, name string, opts LoadOptions) (*Instance, error) {
	if len(opts.Cmd) == 0 {
		return nil, fmt.Errorf("plugin %s: empty Cmd", name)
	}
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = 10 * time.Second
	}

	cookie, err := generateAuthCookie()
	if err != nil {
		return nil, err
	}

	resolved, err := resolvePluginCmd(name, opts.Cmd)
	if err != nil {
		return nil, err
	}

	// Build the subprocess. We don't use CommandContext here because we
	// want to control the kill path explicitly via the lifecycle
	// goroutine — CommandContext would race with Stop().
	cmd := exec.Command(resolved[0], resolved[1:]...)
	cmd.Env = append(append([]string{}, os.Environ()...), opts.Env...)
	cmd.Env = append(cmd.Env,
		EnvHandshake+"="+HandshakeMagic,
		EnvAuthCookie+"="+cookie,
		EnvHostAddr+"="+h.hostAddr,
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %s: stdout pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin %s: start: %w", name, err)
	}

	hs, err := readHandshake(stdout, opts.HandshakeTimeout)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("plugin %s: %w", name, err)
	}

	// Forward remaining stdout to logs so plugins can debug-print.
	go forwardStdout(name, stdout)

	dialCtx, cancel := context.WithTimeout(ctx, opts.HandshakeTimeout)
	defer cancel()
	conn, err := grpc.NewClient(hs.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("plugin %s: dial %s: %w", name, hs.Address, err)
	}
	// grpc.NewClient is non-blocking; verify connectivity by waiting for
	// at least one state transition out of IDLE so we fail fast on a
	// dead plugin rather than at the first call.
	conn.Connect()
	state := conn.GetState()
	for state != connectivity.Ready && dialCtx.Err() == nil {
		if !conn.WaitForStateChange(dialCtx, state) {
			break
		}
		state = conn.GetState()
	}

	client := pb.NewPluginClient(conn)

	// stop closure: stops accepting future calls, kills the process,
	// drains its exit, closes the connection.
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			// Try a graceful Shutdown RPC first; ignore errors.
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutCancel()
			_, _ = client.Shutdown(shutCtx, &pb.ShutdownRequest{})
			_ = conn.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_, _ = cmd.Process.Wait()
		})
	}

	inst, err := registerInitialized(h, ctx, name, cookie, client, stop)
	if err != nil {
		stop()
		return nil, err
	}

	// Lifecycle: when the subprocess exits unexpectedly (plugin crash,
	// killed, etc.), unregister it from the host. The user can then
	// re-LoadPlugin to bring it back.
	go func() {
		_ = cmd.Wait()
		h.Unregister(name)
	}()

	return inst, nil
}

// resolvePluginCmd locates the plugin binary referenced by cmd[0],
// falling back to known-good locations when the configured path doesn't
// exist on disk. Resolution order:
//
//  1. cmd[0] as-is (matches the configured path; covers Docker/installed
//     layouts where the binary lives at an absolute path).
//  2. Sibling of the talon binary (matches the dev layout where
//     `make build` and `make plugins` deposit binaries into bin/).
//  3. PATH lookup on basename (covers Homebrew-style installs where
//     plugins are on PATH).
//
// Each fallback is logged so users see what's happening when the
// configured path is stale (e.g. /usr/local/bin/talon-X-plugin from a
// container config copied to a bare-metal checkout).
//
// On full failure the error names every location searched so the user
// knows where to drop the binary or which path to fix.
func resolvePluginCmd(name string, cmd []string) ([]string, error) {
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

// readHandshake consumes the first stdout line within timeout and
// returns the parsed handshake. The caller keeps the reader for
// subsequent log forwarding.
func readHandshake(r io.Reader, timeout time.Duration) (*HandshakeLine, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 64*1024)

	type result struct {
		hs  *HandshakeLine
		err error
	}
	ch := make(chan result, 1)
	go func() {
		if !scanner.Scan() {
			err := scanner.Err()
			if err == nil {
				err = fmt.Errorf("stdout closed before handshake")
			}
			ch <- result{nil, err}
			return
		}
		hs, err := ParseHandshakeLine(scanner.Text())
		ch <- result{hs, err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return res.hs, nil
	case <-timer.C:
		return nil, fmt.Errorf("handshake timeout after %s", timeout)
	}
}

func forwardStdout(name string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	logger := slog.Default().With("plugin", name)
	for scanner.Scan() {
		// Plugin stdout-after-handshake is free-form text; we just
		// surface it via slog at INFO so it shows up in the same
		// pipeline as everything else. The "plugin" attr lets
		// readers filter to a single subprocess.
		logger.Info("plugin stdout", "line", scanner.Text())
	}
}

// registerInitialized is Host.Register but parameterized so LoadPlugin
// (which already minted a cookie for the env handoff) reuses it instead
// of re-generating. Tests should keep using Host.Register, which
// generates its own cookie.
//
// Like Host.Register, we register the cookie BEFORE calling Initialize
// so the plugin can call back into the host during init (e.g. to read
// config). The bootstrap manifest grants just CAPABILITY_READ_CONFIG;
// the real manifest replaces it once Initialize returns.
func registerInitialized(h *Host, ctx context.Context, name, cookie string, client pb.PluginClient, stop func()) (*Instance, error) {
	bootInst := &Instance{
		Name:     name,
		Cookie:   cookie,
		Manifest: &pb.Manifest{Needs: []pb.Capability{pb.Capability_CAPABILITY_READ_CONFIG}},
		Client:   client,
		stop:     stop,
	}
	h.mu.Lock()
	if _, exists := h.byName[name]; exists {
		h.mu.Unlock()
		return nil, fmt.Errorf("plugin %s: already registered", name)
	}
	h.byCookie[cookie] = bootInst
	h.mu.Unlock()
	rollback := func() {
		h.mu.Lock()
		delete(h.byCookie, cookie)
		h.mu.Unlock()
	}

	resp, err := client.Initialize(ctx, &pb.InitializeRequest{
		AuthCookie:  cookie,
		HostAddress: h.hostAddr,
	})
	if err != nil {
		rollback()
		return nil, fmt.Errorf("plugin %s: initialize: %w", name, err)
	}
	if resp.GetManifest() == nil {
		rollback()
		return nil, fmt.Errorf("plugin %s: initialize returned no manifest", name)
	}
	inst := &Instance{
		Name:     name,
		Cookie:   cookie,
		Manifest: resp.Manifest,
		Client:   client,
		stop:     stop,
	}
	h.mu.Lock()
	if _, exists := h.byName[name]; exists {
		h.mu.Unlock()
		rollback()
		return nil, fmt.Errorf("plugin %s: already registered", name)
	}
	h.byName[name] = inst
	h.byCookie[cookie] = inst
	h.mu.Unlock()
	return inst, nil
}

