package native

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/plugin/pkgutil"
)

// Instance is a registered, running first-party plugin. Mirrors
// legacy.Instance's surface (Name/Manifest/Client/Stop) so callers
// can be migrated incrementally without renaming field references.
type Instance struct {
	Name     string
	Manifest *pb.Manifest
	Client   pb.PluginClient

	client *goplugin.Client // go-plugin handle for lifecycle
	stop   func()
}

// Stop shuts the plugin down idempotently. Best-effort graceful
// Shutdown RPC followed by client.Kill (go-plugin sends SIGTERM,
// then SIGKILL after a brief grace period).
func (i *Instance) Stop() {
	if i == nil || i.stop == nil {
		return
	}
	i.stop()
	i.stop = nil
}

// HostServerFactory builds a per-plugin pb.HostServer for one broker
// connection. LoadPlugin invokes the factory before sending
// Initialize so the host's Host-service is live by the time the
// plugin tries to call back during init. The pluginName is baked
// into the returned server's identity (no cookie lookup needed) so
// capability checks can run against the manifest associated with
// this load.
type HostServerFactory func(pluginName string, manifest *pb.Manifest) pb.HostServer

// Host is the registry for native plugins. One per gateway.
type Host struct {
	mu     sync.RWMutex
	byName map[string]*Instance

	factory HostServerFactory
}

func NewHost(factory HostServerFactory) *Host {
	if factory == nil {
		factory = func(string, *pb.Manifest) pb.HostServer {
			return &pb.UnimplementedHostServer{}
		}
	}
	return &Host{
		byName:  make(map[string]*Instance),
		factory: factory,
	}
}

// LoadOptions is the spawn config. Cmd is the binary + args
// (BuiltinPluginCmd output or a third-party override). Env extends
// the host environment; go-plugin appends the magic cookie itself.
type LoadOptions struct {
	Cmd []string
	Env []string
}

// LoadPlugin spawns name via go-plugin (AutoMTLS on), waits for
// handshake, stands up a per-plugin Host gRPC server on the broker,
// runs Initialize, registers the instance, and starts the lifecycle
// watcher that unregisters on plugin exit.
func (h *Host) LoadPlugin(ctx context.Context, name string, opts LoadOptions) (*Instance, error) {
	if len(opts.Cmd) == 0 {
		return nil, fmt.Errorf("plugin %s: empty Cmd", name)
	}
	resolved, err := pkgutil.ResolvePluginCmd(name, opts.Cmd)
	if err != nil {
		return nil, err
	}

	gp := &grpcPlugin{} // host side — Impl stays nil, GRPCClient fills hostBroker
	cmd := exec.Command(resolved[0], resolved[1:]...)
	cmd.Env = append(cmd.Env, opts.Env...)

	logger := slog.With("plugin", name)
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{1: {PluginMapKey: gp}},
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		AutoMTLS:         true,
		Logger:           newHCLogAdapter(logger),
	})

	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin %s: client start: %w", name, err)
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

	if gp.hostBroker == nil {
		client.Kill()
		return nil, fmt.Errorf("plugin %s: GRPCClient ran without populating broker (go-plugin internals changed?)", name)
	}

	// Stand up the per-plugin Host service on the broker BEFORE
	// Initialize so the plugin can call back during init without
	// racing. Manifest is empty at first (so any init-time call is
	// rejected); Initialize's response swaps in the real manifest
	// via the holder. The interceptor reads holder.Get() per-call,
	// so the swap takes effect without re-serving.
	holder := NewManifestHolder(nil)
	brokerID := gp.hostBroker.NextId()
	hostSvc := h.factory(name, holder.Get())
	interceptor := NewCapabilityInterceptor(name, holder)
	go gp.hostBroker.AcceptAndServe(brokerID, func(serverOpts []grpc.ServerOption) *grpc.Server {
		serverOpts = append(serverOpts, grpc.UnaryInterceptor(interceptor))
		s := grpc.NewServer(serverOpts...)
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
	manifest := resp.GetManifest()
	if manifest == nil {
		client.Kill()
		return nil, fmt.Errorf("plugin %s: Initialize returned no manifest", name)
	}
	holder.Set(manifest)

	inst := &Instance{
		Name:     name,
		Manifest: manifest,
		Client:   pluginClient,
		client:   client,
		stop: func() {
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

	// Lifecycle: poll client.Exited() (go-plugin has no done channel)
	// and unregister on transition. 1s cadence is fine — plugin
	// crashes are rare, and the unregister itself is only needed to
	// permit a re-LoadPlugin to take the name.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			if client.Exited() {
				h.Unregister(name)
				return
			}
		}
	}()

	return inst, nil
}

// Unregister removes name from the registry. Called both by user
// Stop() flows and by the lifecycle goroutine on client.Exited.
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

// List returns the names of currently-registered plugins.
func (h *Host) List() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.byName))
	for n := range h.byName {
		out = append(out, n)
	}
	return out
}
