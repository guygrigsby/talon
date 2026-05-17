package native

import (
	"context"
	"fmt"
	"sync"

	goplugin "github.com/hashicorp/go-plugin"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
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

// LoadPlugin spawns name via go-plugin, waits for handshake + mTLS,
// stands up a per-plugin Host gRPC server on the broker, calls
// Initialize, registers the instance, and starts the lifecycle
// watcher. Implementation lands in Task 6.
func (h *Host) LoadPlugin(ctx context.Context, name string, opts LoadOptions) (*Instance, error) {
	_ = ctx
	_ = opts
	return nil, fmt.Errorf("plugin %s: native.LoadPlugin not yet implemented (talon-e4h Task 6)", name)
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
