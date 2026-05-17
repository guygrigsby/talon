package legacy

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/plugin/pkgutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Instance is a registered, initialized plugin. The fields cover both
// in-process (test) and subprocess (production) registration paths —
// Cmd is nil for in-process plugins and tests use that fact to skip
// process-lifecycle logic.
type Instance struct {
	Name     string
	Cookie   string
	Manifest *pb.Manifest
	Client   pb.PluginClient

	// stop releases the connection and any subprocess resources. Set by
	// whichever Register* method created the instance.
	stop func()
}

// Stop closes the plugin's connection and (when applicable) terminates
// its subprocess. Idempotent.
func (i *Instance) Stop() {
	if i == nil || i.stop == nil {
		return
	}
	i.stop()
	i.stop = nil
}

// Host is the registry + capability gate for plugins. Instantiate one per
// gateway. Wire UnaryInterceptor/StreamInterceptor onto the gRPC server
// that exposes the Host service.
type Host struct {
	mu       sync.RWMutex
	byName   map[string]*Instance
	byCookie map[string]*Instance

	// hostAddr is the address (host:port) plugins dial for the Host
	// service. Passed to plugins via EnvHostAddr + InitializeRequest.
	// Empty means "no host service published yet" — a plugin that tries
	// to call back will get a connection error rather than a security
	// problem.
	hostAddr string
}

// NewHost returns an empty registry. hostAddr is what plugins will dial
// when they need to call back into the host (typically
// "127.0.0.1:<port>" of the Host gRPC server).
func NewHost(hostAddr string) *Host {
	return &Host{
		byName:   make(map[string]*Instance),
		byCookie: make(map[string]*Instance),
		hostAddr: hostAddr,
	}
}

// HostAddr returns the host service address plugins are told to dial.
func (h *Host) HostAddr() string { return h.hostAddr }

// Register attaches a plugin to the registry by performing the
// Initialize RPC and recording the returned manifest. Lower-level than
// LoadPlugin: callers that already have a Plugin client (e.g. tests
// using bufconn) skip the spawn-and-dial path here.
//
// stop is invoked when the plugin is unregistered (e.g. on Host shutdown
// or plugin process exit). It should release the gRPC client and any
// owned subprocess resources.
func (h *Host) Register(ctx context.Context, name string, client pb.PluginClient, stop func()) (*Instance, error) {
	if name == "" {
		return nil, errors.New("plugin host: name is required")
	}
	cookie, err := generateAuthCookie()
	if err != nil {
		return nil, err
	}
	// Register the cookie BEFORE calling Initialize so the plugin can
	// call back into the host during init (typical case: a config
	// read to decide what to register). The bootstrap manifest grants
	// just enough capability for that read; the real manifest lands
	// after Initialize returns and replaces this one.
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
		if stop != nil {
			stop()
		}
		return nil, fmt.Errorf("plugin %s: already registered", name)
	}
	h.byCookie[cookie] = bootInst
	h.mu.Unlock()
	// Roll back the bootstrap registration on any failure path so a
	// rejected plugin doesn't leave a phantom cookie that another
	// caller could exploit.
	rollback := func() {
		h.mu.Lock()
		delete(h.byCookie, cookie)
		h.mu.Unlock()
		if stop != nil {
			stop()
		}
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
		// Lost the race against another LoadPlugin call with the same
		// name — rare but theoretically possible. Drop and roll back.
		h.mu.Unlock()
		rollback()
		return nil, fmt.Errorf("plugin %s: already registered", name)
	}
	h.byName[name] = inst
	h.byCookie[cookie] = inst
	h.mu.Unlock()
	return inst, nil
}

// Unregister removes the plugin from the registry and invokes its stop
// callback. Idempotent.
func (h *Host) Unregister(name string) {
	h.mu.Lock()
	inst, ok := h.byName[name]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.byName, name)
	delete(h.byCookie, inst.Cookie)
	h.mu.Unlock()
	inst.Stop()
}

// Get returns the plugin registered under name, or nil if absent.
func (h *Host) Get(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byName[name]
}

// List returns the registered plugin names in arbitrary order.
func (h *Host) List() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.byName))
	for n := range h.byName {
		out = append(out, n)
	}
	return out
}

// ProviderByName returns the plugin instance whose manifest offers a
// provider by the given key, or nil if no loaded plugin advertises it.
// First match wins (manifests should pick non-conflicting names; on
// collision the host itself doesn't pick a winner).
func (h *Host) ProviderByName(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, inst := range h.byName {
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, ps := range inst.Manifest.OffersProviders {
			if ps.GetName() == name {
				return inst
			}
		}
	}
	return nil
}

// ImageProviderByName returns the plugin instance whose manifest offers
// an image provider by the given key, or nil if no loaded plugin
// advertises it. Symmetric to ProviderByName.
func (h *Host) ImageProviderByName(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, inst := range h.byName {
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, ps := range inst.Manifest.OffersImageProviders {
			if ps.GetName() == name {
				return inst
			}
		}
	}
	return nil
}

// ChannelByName returns the plugin instance whose manifest offers a
// channel by the given name, or nil if no loaded plugin advertises it.
// Symmetric to ProviderByName.
func (h *Host) ChannelByName(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, inst := range h.byName {
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, ch := range inst.Manifest.OffersChannels {
			if ch == name {
				return inst
			}
		}
	}
	return nil
}

// Shutdown unregisters every plugin (invoking each stop callback). Call
// this on gateway shutdown so subprocesses get a clean exit signal.
func (h *Host) Shutdown() {
	h.mu.Lock()
	insts := make([]*Instance, 0, len(h.byName))
	for _, inst := range h.byName {
		insts = append(insts, inst)
	}
	h.byName = make(map[string]*Instance)
	h.byCookie = make(map[string]*Instance)
	h.mu.Unlock()
	for _, inst := range insts {
		inst.Stop()
	}
}

// =============================================================================
// Capability interceptor
// =============================================================================

// callerInstance resolves the calling plugin from gRPC metadata. Returns
// the instance + nil on success; ("", err) status when the cookie is
// missing or unknown. Called by both unary and stream interceptors.
func (h *Host) callerInstance(ctx context.Context) (*Instance, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "plugin: missing metadata")
	}
	cookies := md.Get(CookieMetadataKey)
	if len(cookies) == 0 {
		return nil, status.Error(codes.Unauthenticated, "plugin: missing auth cookie")
	}
	h.mu.RLock()
	inst, ok := h.byCookie[cookies[0]]
	h.mu.RUnlock()
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "plugin: unrecognized auth cookie")
	}
	return inst, nil
}

// permits is a thin shim over pkgutil.Permits kept for call-site
// brevity inside this package's interceptors.
func permits(m *pb.Manifest, c pb.Capability) bool {
	return pkgutil.Permits(m, c)
}

// UnaryInterceptor returns a grpc.UnaryServerInterceptor that
// authenticates the calling plugin via the auth cookie metadata and
// gates the request against the method's required capability.
func (h *Host) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		inst, err := h.callerInstance(ctx)
		if err != nil {
			return nil, err
		}
		need, ok := pkgutil.MethodCapability[info.FullMethod]
		if !ok {
			// Closed set: an unmapped method must not pass — that's a
			// programming error worth failing loudly.
			return nil, status.Errorf(codes.Internal, "plugin: method %s has no capability mapping", info.FullMethod)
		}
		if !permits(inst.Manifest, need) {
			return nil, status.Errorf(codes.PermissionDenied,
				"plugin %q lacks capability %s for %s", inst.Name, need, info.FullMethod)
		}
		// Stamp ctx with the resolved plugin name so the handler can
		// scope responses without re-parsing metadata.
		ctx = withCallerName(ctx, inst.Name)
		return handler(ctx, req)
	}
}

// StreamInterceptor mirrors UnaryInterceptor for server-streaming Host
// methods. None of the current Host methods are streams, but the
// interceptor is wired now so adding one doesn't accidentally bypass
// the gate.
func (h *Host) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		inst, err := h.callerInstance(ss.Context())
		if err != nil {
			return err
		}
		need, ok := pkgutil.MethodCapability[info.FullMethod]
		if !ok {
			return status.Errorf(codes.Internal, "plugin: method %s has no capability mapping", info.FullMethod)
		}
		if !permits(inst.Manifest, need) {
			return status.Errorf(codes.PermissionDenied,
				"plugin %q lacks capability %s for %s", inst.Name, need, info.FullMethod)
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: withCallerName(ss.Context(), inst.Name)})
	}
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// callerNameKey carries the resolved plugin name through ctx so handlers
// can scope responses without re-parsing metadata.
type callerNameKey struct{}

func withCallerName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, callerNameKey{}, name)
}

// CallerNameFromContext returns the plugin name resolved by the
// interceptor on the inbound request. Empty string means the request
// didn't come through the interceptor (e.g. internal call).
func CallerNameFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(callerNameKey{}).(string); ok {
		return v
	}
	return ""
}
