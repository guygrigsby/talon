// Package native is the hashicorp/go-plugin-based host and serve
// implementation for Talon plugins.
//
// The host and plugin process share the same .proto
// (internal/plugin/pb) and capability map (internal/plugin/pkgutil).
package native

import (
	"context"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// Handshake is the magic-cookie pair go-plugin uses to refuse bare
// shell invocations.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "TALON_PLUGIN_HANDSHAKE",
	MagicCookieValue: "talon.plugin.v1",
}

// PluginMapKey is the registry key both ends use for talon's Plugin
// service. go-plugin allows multiple plugin types per binary; we ship
// exactly one (the Plugin service), so the map always has this key.
const PluginMapKey = "talon-plugin"

// grpcPlugin implements goplugin.GRPCPlugin for the Plugin service.
// The plugin-side instance holds Impl (pb.PluginServer) which it
// registers in GRPCServer; the host-side instance returns a
// pb.PluginClient from GRPCClient. Both ends capture the GRPCBroker
// for the bidi Host-service channel — plugins dial back via the
// broker id the host sends in InitializeRequest.
type grpcPlugin struct {
	goplugin.Plugin
	Impl pb.PluginServer // set on plugin side; nil on host side

	// hostBroker is the GRPCBroker the host side captures in
	// GRPCClient. LoadPlugin reads it to AcceptAndServe a per-plugin
	// HostServer instance. Each grpcPlugin instance is owned by one
	// plugin load, so this is goroutine-safe by virtue of single
	// ownership through LoadPlugin's setup phase.
	hostBroker *goplugin.GRPCBroker
}

func (p *grpcPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	// Plugin side: stash on the package-global accessor too, so
	// HostClientHolder (which doesn't know about grpcPlugin) can
	// reach the broker during Initialize. Safe because each plugin
	// process has at most one Plugin-service registration.
	setCurrentBroker(broker)
	pb.RegisterPluginServer(s, p.Impl)
	return nil
}

func (p *grpcPlugin) GRPCClient(_ context.Context, broker *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	p.hostBroker = broker
	return pb.NewPluginClient(c), nil
}
