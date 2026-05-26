//go:build e2e

// Package e2e drives talon-gateway inside a Docker container via
// testcontainers-go. The harness exists primarily to exercise plugin
// paths end-to-end: a real subprocess plugin attached to a real
// gateway, with the same wire-protocol the user sees, on Linux
// regardless of host OS.
//
// Why containers (rather than just running the binary in-process):
//   - Cross-platform: tests run identically on macOS dev machines and
//     Linux CI without per-host setup.
//   - True process isolation: plugins crash without taking the test
//     harness down, and filesystem side effects stay inside the
//     container.
//   - Native plugin coverage: tests exercise the same subprocess
//     boundary the production gateway uses.
//
// Tests that use this package run under -tags=e2e or by leaving the
// build tag off and skipping when Docker isn't reachable. Either path
// keeps the default `go test ./...` invocation fast.
package e2e

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/guygrigsby/talon/internal/talonconfig"
)

// Gateway is one running talon-gateway container plus its log buffer.
// Lifecycle: StartGateway → (test work) → t.Cleanup auto-stops.
type Gateway struct {
	Container testcontainers.Container

	// Address is the host:port the test can dial to reach the gateway's
	// WebSocket endpoint. testcontainers maps the EXPOSE-d port to a
	// random ephemeral port on the loopback host bridge.
	Address string

	t      *testing.T
	logsMu sync.Mutex
	logs   []string
}

// GatewayOpts is the per-test configuration. Two fields users typically
// care about:
//
//	ConfigJSON: a runtime-shaped JSON fixture converted to config.toml
//	            before mounting. Tests build this per-scenario.
//
//	ExtraCmd:   appended to the entrypoint's default CMD. Useful for
//	            --token, --tailscale, etc. when a test needs them.
type GatewayOpts struct {
	// ConfigJSON is a runtime-shaped JSON fixture. Required.
	ConfigJSON []byte

	// ExtraCmd appends extra args to the gateway run invocation.
	ExtraCmd []string

	// StartupTimeout caps how long StartGateway will wait for the
	// "talon gateway listening" log line before failing the test.
	// Zero means use the default (60s).
	StartupTimeout time.Duration
}

// imageOnce builds the Dockerfile.test image at most once per test
// process. Subsequent StartGateway calls reuse it. testcontainers
// itself cooperates with docker's layer cache so this is mostly a
// safety net for tests that hammer multiple Gateways.
var (
	imageOnce sync.Once
	imageRef  string
	imageErr  error
)

// projectRoot returns the talon repo root by walking up from the
// current working directory looking for go.mod. Test execution may
// happen from any sub-package (`go test ./internal/e2e/...`), so the
// build context for the Dockerfile.test must be discovered.
func projectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", errors.New("could not locate project root (no go.mod above " + wd + ")")
		}
		wd = parent
	}
}

// ensureImage builds (or rebuilds) the talon e2e image. Returns the
// tag testcontainers should use. Cached per process via imageOnce.
func ensureImage(ctx context.Context) (string, error) {
	imageOnce.Do(func() {
		root, err := projectRoot()
		if err != nil {
			imageErr = err
			return
		}
		req := testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				FromDockerfile: testcontainers.FromDockerfile{
					Context:    root,
					Dockerfile: "Dockerfile.test",
					KeepImage:  true,
				},
			},
		}
		// We don't actually start the throwaway container — just
		// trigger the image build and capture the resolved name.
		// testcontainers' BuildImage is the right primitive for this
		// but isn't exposed prior to the GenericContainer build path,
		// so the simplest portable approach is to start+stop.
		c, err := testcontainers.GenericContainer(ctx, req)
		if err != nil {
			imageErr = fmt.Errorf("build e2e image: %w", err)
			return
		}
		// Inspect for the image tag name. The InspectResponse embeds
		// docker's container.Config which carries the .Image field.
		insp, ierr := c.Inspect(ctx)
		if ierr == nil && insp != nil && insp.Config != nil && insp.Config.Image != "" {
			imageRef = insp.Config.Image
		}
		_ = c.Terminate(ctx)
		if imageRef == "" {
			imageErr = errors.New("could not resolve built image name")
			return
		}
	})
	return imageRef, imageErr
}

// StartGateway boots a container and waits for the gateway to declare
// it's listening. Test cleanup auto-terminates. Skips the test when
// Docker isn't reachable (so `go test ./...` on a machine without
// Docker doesn't fail the whole run).
func StartGateway(t *testing.T, opts GatewayOpts) *Gateway {
	t.Helper()
	if opts.ConfigJSON == nil {
		t.Fatal("StartGateway: opts.ConfigJSON is required")
	}
	cfg, err := talonconfig.FromRuntimeJSON(opts.ConfigJSON)
	if err != nil {
		t.Fatalf("StartGateway: convert fixture config: %v", err)
	}
	configTOML := talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Build the image once per process. Failures here usually mean
	// "Docker isn't running" — surface as Skip rather than Fail so the
	// suite stays usable on Docker-less machines.
	image, err := ensureImage(ctx)
	if err != nil {
		t.Skipf("e2e: cannot build talon image (Docker not reachable?): %v", err)
	}

	startTimeout := opts.StartupTimeout
	if startTimeout == 0 {
		startTimeout = 60 * time.Second
	}

	cmd := []string{"gateway", "run", "--bind=lan", "--port=18789", "--auth=none", "--allow-unconfigured"}
	cmd = append(cmd, opts.ExtraCmd...)

	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			ExposedPorts: []string{"18789/tcp"},
			Cmd:          cmd,
			Env: map[string]string{
				"TALON_STATE_DIR": "/root/.talon",
			},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            strings.NewReader(string(configTOML)),
					ContainerFilePath: "/root/.talon/config.toml",
					FileMode:          0644,
				},
			},
			WaitingFor: wait.ForLog("talon gateway listening").WithStartupTimeout(startTimeout),
		},
		Started: true,
	}

	cnt, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		t.Fatalf("start gateway container: %v", err)
	}
	t.Cleanup(func() { _ = cnt.Terminate(context.Background()) })

	host, err := cnt.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	mappedPort, err := cnt.MappedPort(ctx, "18789/tcp")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	addr := host + ":" + mappedPort.Port()

	g := &Gateway{
		Container: cnt,
		Address:   addr,
		t:         t,
	}
	g.startLogStream(ctx)
	return g
}

// startLogStream pulls the container's combined stdout/stderr into
// g.logs as lines arrive. Lets WaitForLog poll a buffered slice
// instead of re-fetching docker logs each iteration.
func (g *Gateway) startLogStream(ctx context.Context) {
	rdr, err := g.Container.Logs(ctx)
	if err != nil {
		// Logs may be transiently unavailable (race with container
		// startup). Tests that care can call Logs() directly.
		g.t.Logf("e2e: container logs unavailable: %v", err)
		return
	}
	go func() {
		defer rdr.Close()
		scanner := bufio.NewScanner(rdr)
		// Increase scanner buffer for long log lines (config dumps).
		scanner.Buffer(make([]byte, 1<<14), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			g.logsMu.Lock()
			g.logs = append(g.logs, line)
			g.logsMu.Unlock()
		}
	}()
}

// Logs returns a snapshot of every log line observed so far. Safe to
// call concurrently with the streaming goroutine.
func (g *Gateway) Logs() []string {
	g.logsMu.Lock()
	defer g.logsMu.Unlock()
	out := make([]string, len(g.logs))
	copy(out, g.logs)
	return out
}

// LogsString returns Logs() joined by "\n" — convenient for `strings.Contains`
// assertions in tests.
func (g *Gateway) LogsString() string {
	return strings.Join(g.Logs(), "\n")
}

// WaitForLog blocks until any logged line contains substr or the
// timeout fires. Returns the first matching line on success.
func (g *Gateway) WaitForLog(substr string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, line := range g.Logs() {
			if strings.Contains(line, substr) {
				return line, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for log %q after %s", substr, timeout)
		}
		<-tick.C
	}
}

// dumpLogsTo is a debugging hatch — used internally by tests in this
// package via a helper that prints the captured logs when an
// assertion fails. Kept exported because future tests outside this
// package may want it.
func (g *Gateway) DumpLogsTo(w io.Writer) {
	for _, line := range g.Logs() {
		fmt.Fprintln(w, line)
	}
}
