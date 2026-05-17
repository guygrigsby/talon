package native

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/guygrigsby/talon/internal/plugin/legacy"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/plugin/pkgutil"
)

// HostServerFactory builds a per-plugin pb.HostServer for one broker
// connection. Spawn invokes the factory before sending Initialize so
// the host's Host-service is live when the plugin tries to call back
// during init. pluginName is baked into the returned server (no
// cookie lookup needed) so capability checks resolve against the
// manifest this load returns. The initial manifest is empty; Spawn
// swaps the real one into the holder when Initialize succeeds.
type HostServerFactory func(pluginName string, holder *ManifestHolder) pb.HostServer

// LoadOptions is the spawn config. Cmd is the binary + args
// (BuiltinPluginCmd output or a third-party override). Env extends
// the host environment; go-plugin appends the magic cookie itself.
type LoadOptions struct {
	Cmd []string
	Env []string
}

// Spawn launches name via go-plugin (AutoMTLS on), stands up a
// per-plugin Host gRPC server on the broker, runs Initialize, and
// returns a registered *legacy.Instance the caller publishes into
// the shared legacy.Host registry.
//
// Returns the Instance even when the caller never calls
// legacy.Host.RegisterInstance — the lifecycle watcher inside Spawn
// keeps polling client.Exited and invokes the caller-supplied
// onExit hook when the subprocess goes away. Pass a nil onExit if
// no cleanup is needed (rare).
func Spawn(
	ctx context.Context,
	name string,
	factory HostServerFactory,
	opts LoadOptions,
	onExit func(name string),
) (*legacy.Instance, error) {
	if len(opts.Cmd) == 0 {
		return nil, fmt.Errorf("plugin %s: empty Cmd", name)
	}
	if factory == nil {
		factory = defaultHostServerFactory
	}
	resolved, err := pkgutil.ResolvePluginCmd(name, opts.Cmd)
	if err != nil {
		return nil, err
	}

	gp := &grpcPlugin{} // host side — Impl nil; GRPCClient fills hostBroker
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
		return nil, fmt.Errorf("plugin %s: GRPCClient ran without populating broker", name)
	}

	holder := NewManifestHolder(nil)
	hostSvc := factory(name, holder)
	interceptor := NewCapabilityInterceptor(name, holder)
	brokerID := gp.hostBroker.NextId()
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

	inst := legacy.NewInstance(legacy.InstanceFields{
		Name:     name,
		Manifest: manifest,
		Client:   pluginClient,
		Stop: func() {
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutCancel()
			_, _ = pluginClient.Shutdown(shutCtx, &pb.ShutdownRequest{})
			client.Kill()
		},
	})

	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			if client.Exited() {
				if onExit != nil {
					onExit(name)
				}
				return
			}
		}
	}()

	return inst, nil
}

// defaultHostServerFactory yields a no-op pb.HostServer for callers
// that don't supply one. The interceptor still gates calls against
// the empty manifest, so this is effectively "no callbacks allowed."
func defaultHostServerFactory(string, *ManifestHolder) pb.HostServer {
	return &pb.UnimplementedHostServer{}
}
