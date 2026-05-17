package native

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/plugin/pkgutil"
)

// NewCapabilityInterceptor returns a grpc.UnaryServerInterceptor that
// gates a per-plugin broker connection against the plugin's manifest.
// Unlike the legacy interceptor (which extracts identity from a cookie
// in metadata), the native broker channel is already bound to a
// specific plugin at AcceptAndServe time, so identity is captured by
// closure rather than looked up.
//
// Unknown methods (not in pkgutil.MethodCapability) fail with
// Internal — that's a programming error, not a runtime failure.
// Missing capabilities fail with PermissionDenied citing the plugin
// name and required capability.
func NewCapabilityInterceptor(pluginName string, manifest *pb.Manifest) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		need, ok := pkgutil.MethodCapability[info.FullMethod]
		if !ok {
			return nil, status.Errorf(codes.Internal, "plugin: method %s has no capability mapping", info.FullMethod)
		}
		if !pkgutil.Permits(manifest, need) {
			return nil, status.Errorf(codes.PermissionDenied,
				"plugin %q lacks capability %s for %s", pluginName, need, info.FullMethod)
		}
		return handler(ctx, req)
	}
}
