package native

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/plugin/pkgutil"
)

// ManifestHolder is a goroutine-safe slot for the manifest a
// per-plugin Host-service interceptor gates against. LoadPlugin
// installs an empty manifest before sending Initialize (so any
// init-time callback is rejected by Permits), then swaps in the real
// manifest the plugin returns. Method gating uses whichever manifest
// is current at request time, so an Initialize race doesn't allow a
// bootstrap call to escape the empty-manifest rejection.
type ManifestHolder struct {
	v atomic.Pointer[pb.Manifest]
}

func NewManifestHolder(initial *pb.Manifest) *ManifestHolder {
	h := &ManifestHolder{}
	h.Set(initial)
	return h
}

func (h *ManifestHolder) Set(m *pb.Manifest) {
	if m == nil {
		m = &pb.Manifest{}
	}
	h.v.Store(m)
}

func (h *ManifestHolder) Get() *pb.Manifest {
	return h.v.Load()
}

// NewCapabilityInterceptor returns a grpc.UnaryServerInterceptor that
// gates a per-plugin broker connection against the plugin's current
// manifest. The native broker channel is bound to a specific plugin at
// AcceptAndServe time, so identity is captured by closure. The manifest
// is read through holder on each call so LoadPlugin's post-Initialize
// swap takes effect without re-serving the gRPC server.
//
// Unknown methods (not in pkgutil.MethodCapability) fail with
// Internal — that's a programming error, not a runtime failure.
// Missing capabilities fail with PermissionDenied citing the plugin
// name and required capability.
func NewCapabilityInterceptor(pluginName string, holder *ManifestHolder) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		need, ok := pkgutil.MethodCapability[info.FullMethod]
		if !ok {
			return nil, status.Errorf(codes.Internal, "plugin: method %s has no capability mapping", info.FullMethod)
		}
		if !pkgutil.Permits(holder.Get(), need) {
			return nil, status.Errorf(codes.PermissionDenied,
				"plugin %q lacks capability %s for %s", pluginName, need, info.FullMethod)
		}
		return handler(ctx, req)
	}
}
