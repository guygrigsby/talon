package legacy

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// fakePluginServer is the in-process counterpart to testplugin/main.go —
// reused by the unit tests below to avoid spawning subprocesses for
// pure logic tests.
type fakePluginServer struct {
	pb.UnimplementedPluginServer
	manifest *pb.Manifest
	initErr  error
}

func (f *fakePluginServer) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	if f.initErr != nil {
		return nil, f.initErr
	}
	return &pb.InitializeResponse{Manifest: f.manifest}, nil
}

func (f *fakePluginServer) Shutdown(ctx context.Context, req *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	return &pb.ShutdownResponse{}, nil
}

// dialFake spins up a fakePluginServer on a bufconn and returns a
// connected client.
func dialFake(t *testing.T, fp *fakePluginServer) (pb.PluginClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 16)
	srv := grpc.NewServer()
	pb.RegisterPluginServer(srv, fp)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pb.NewPluginClient(conn), func() {
		_ = conn.Close()
		srv.Stop()
	}
}

// --- registry behavior ----------------------------------------------------

func TestHost_RegisterAndLookup(t *testing.T) {
	manifest := &pb.Manifest{
		Name:    "telegram",
		Version: "0.1.0",
		Needs:   []pb.Capability{pb.Capability_CAPABILITY_SEND_CHANNEL_MESSAGE},
	}
	client, cleanup := dialFake(t, &fakePluginServer{manifest: manifest})
	defer cleanup()

	h := NewHost("127.0.0.1:18790")
	inst, err := h.Register(t.Context(), "telegram", client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Name != "telegram" || inst.Manifest.Name != "telegram" {
		t.Errorf("wrong instance: %+v", inst)
	}
	if got := h.Get("telegram"); got != inst {
		t.Errorf("Get returned different instance")
	}
	names := h.List()
	if len(names) != 1 || names[0] != "telegram" {
		t.Errorf("List = %v, want [telegram]", names)
	}
	// Cookie must be present and 48-char hex (24 bytes).
	if len(inst.Cookie) != 48 {
		t.Errorf("cookie length = %d, want 48", len(inst.Cookie))
	}
}

func TestHost_RegisterRejectsDuplicateName(t *testing.T) {
	client, cleanup := dialFake(t, &fakePluginServer{manifest: &pb.Manifest{Name: "x"}})
	defer cleanup()

	h := NewHost("")
	if _, err := h.Register(t.Context(), "x", client, nil); err != nil {
		t.Fatal(err)
	}
	_, err := h.Register(t.Context(), "x", client, nil)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("expected duplicate-name rejection, got %v", err)
	}
}

func TestHost_RegisterFailsOnEmptyManifest(t *testing.T) {
	client, cleanup := dialFake(t, &fakePluginServer{manifest: nil})
	defer cleanup()

	h := NewHost("")
	_, err := h.Register(t.Context(), "x", client, nil)
	if err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Errorf("expected no-manifest error, got %v", err)
	}
}

func TestHost_RegisterFailsOnInitializeError(t *testing.T) {
	client, cleanup := dialFake(t, &fakePluginServer{initErr: status.Errorf(7, "denied")})
	defer cleanup()

	h := NewHost("")
	_, err := h.Register(t.Context(), "x", client, nil)
	if err == nil {
		t.Errorf("expected initialize error to propagate")
	}
}

func TestHost_UnregisterCallsStop(t *testing.T) {
	client, cleanup := dialFake(t, &fakePluginServer{manifest: &pb.Manifest{Name: "x"}})
	defer cleanup()

	stopped := false
	h := NewHost("")
	if _, err := h.Register(t.Context(), "x", client, func() { stopped = true }); err != nil {
		t.Fatal(err)
	}
	h.Unregister("x")
	if !stopped {
		t.Errorf("Unregister should have invoked stop callback")
	}
	if h.Get("x") != nil {
		t.Errorf("Get should return nil after Unregister")
	}
}

// --- capability interceptor ----------------------------------------------

func TestUnaryInterceptor_GrantsAccessWhenCapabilityHeld(t *testing.T) {
	client, cleanup := dialFake(t, &fakePluginServer{manifest: &pb.Manifest{
		Name:  "x",
		Needs: []pb.Capability{pb.Capability_CAPABILITY_READ_CONFIG},
	}})
	defer cleanup()

	h := NewHost("")
	inst, _ := h.Register(t.Context(), "x", client, nil)

	intercept := h.UnaryInterceptor()
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		// Verify the resolved plugin name was stamped on ctx.
		if got := CallerNameFromContext(ctx); got != "x" {
			t.Errorf("CallerNameFromContext = %q, want x", got)
		}
		return "ok", nil
	}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(CookieMetadataKey, inst.Cookie))
	res, err := intercept(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/talon.plugin.v1.Host/GetConfig"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if !called || res != "ok" {
		t.Errorf("handler not called or returned wrong value: %v %v", called, res)
	}
}

func TestUnaryInterceptor_DeniesWhenCapabilityMissing(t *testing.T) {
	client, cleanup := dialFake(t, &fakePluginServer{manifest: &pb.Manifest{
		Name:  "x",
		Needs: []pb.Capability{pb.Capability_CAPABILITY_LIST_AGENTS}, // wants A, asks for B
	}})
	defer cleanup()

	h := NewHost("")
	inst, _ := h.Register(t.Context(), "x", client, nil)

	intercept := h.UnaryInterceptor()
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(CookieMetadataKey, inst.Cookie))
	_, err := intercept(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/talon.plugin.v1.Host/GetConfig"}, dummyHandler)
	if err == nil || status.Code(err).String() != "PermissionDenied" {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
	if !strings.Contains(err.Error(), "CAPABILITY_READ_CONFIG") {
		t.Errorf("error should name the missing capability: %v", err)
	}
}

func TestUnaryInterceptor_DeniesUnknownCookie(t *testing.T) {
	h := NewHost("")
	intercept := h.UnaryInterceptor()
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(CookieMetadataKey, "bogus"))
	_, err := intercept(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/talon.plugin.v1.Host/GetConfig"}, dummyHandler)
	if status.Code(err).String() != "Unauthenticated" {
		t.Errorf("expected Unauthenticated, got %v (%v)", err, status.Code(err))
	}
}

func TestUnaryInterceptor_DeniesMissingCookie(t *testing.T) {
	h := NewHost("")
	intercept := h.UnaryInterceptor()
	_, err := intercept(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/talon.plugin.v1.Host/GetConfig"}, dummyHandler)
	if status.Code(err).String() != "Unauthenticated" {
		t.Errorf("expected Unauthenticated for missing metadata, got %v", err)
	}
}

func TestUnaryInterceptor_FailsOnUnmappedMethod(t *testing.T) {
	client, cleanup := dialFake(t, &fakePluginServer{manifest: &pb.Manifest{Name: "x"}})
	defer cleanup()
	h := NewHost("")
	inst, _ := h.Register(t.Context(), "x", client, nil)

	intercept := h.UnaryInterceptor()
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(CookieMetadataKey, inst.Cookie))
	_, err := intercept(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/talon.plugin.v1.Host/Unmapped"}, dummyHandler)
	if status.Code(err).String() != "Internal" {
		t.Errorf("unmapped method must be Internal-flagged (closed-set rule), got %v", err)
	}
}

// --- handshake parsing ----------------------------------------------------

func TestParseHandshakeLine_Valid(t *testing.T) {
	hs, err := ParseHandshakeLine("1|TCP|127.0.0.1:54321|grpc")
	if err != nil {
		t.Fatal(err)
	}
	if hs.Version != 1 || hs.Network != "TCP" || hs.Address != "127.0.0.1:54321" || hs.Protocol != "grpc" {
		t.Errorf("parsed wrong: %+v", hs)
	}
}

func TestParseHandshakeLine_Invalid(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "expected 4 fields"},
		{"1|TCP|127.0.0.1:54321", "expected 4 fields"},
		{"abc|TCP|127.0.0.1:54321|grpc", "invalid version"},
		{"99|TCP|127.0.0.1:54321|grpc", "unsupported version"},
		{"1||127.0.0.1:54321|grpc", "empty network"},
		{"1|TCP|127.0.0.1:54321|http", "unsupported protocol"},
	}
	for _, tc := range cases {
		_, err := ParseHandshakeLine(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseHandshakeLine(%q) = %v, want error containing %q", tc.in, err, tc.want)
		}
	}
}

// --- helpers --------------------------------------------------------------

func dummyHandler(ctx context.Context, req any) (any, error) { return "ok", nil }

// Sanity check: the Host satisfies the gRPC-server-side interceptor
// signatures (compile-time only).
var _ = func() bool {
	var h *Host
	var _ grpc.UnaryServerInterceptor = h.UnaryInterceptor()
	var _ grpc.StreamServerInterceptor = h.StreamInterceptor()
	return true
}

// Compile-time check: errors.New is referenced so unused-import lints
// don't trip when we trim cases above.
var _ = errors.New
