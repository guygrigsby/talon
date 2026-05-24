package connectapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	talonv1connect "github.com/guygrigsby/talon/internal/api/v1/talon/v1/talonv1connect"
	"github.com/guygrigsby/talon/internal/server"
)

// Auth interceptor tests use a tiny in-process Connect server with
// only the Infra service mounted — that's enough to cover the
// gating logic without booting a real gateway. The Health method
// is exempt by design; NodeList is the canary that verifies the
// gate fires on non-health calls.

type stubInfra struct {
	srv *server.Server
}

func (s *stubInfra) Health(_ context.Context, _ *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.HealthResponse], error) {
	return connect.NewResponse(&talonv1.HealthResponse{Ok: true, Server: "stub", Version: "test"}), nil
}

func (s *stubInfra) NodeList(_ context.Context, _ *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.NodeListResponse], error) {
	return connect.NewResponse(&talonv1.NodeListResponse{}), nil
}

func newAuthTestServer(t *testing.T, cfg server.AuthConfig) (*httptest.Server, *http.Client) {
	t.Helper()
	mux := http.NewServeMux()
	var opts []connect.HandlerOption
	if ai := newAuthInterceptor(cfg); ai != nil {
		opts = append(opts, connect.WithInterceptors(ai))
	}
	mux.Handle(talonv1connect.NewInfraServiceHandler(&stubInfra{}, opts...))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, ts.Client()
}

func TestAuthInterceptor_NoneMode_Disabled(t *testing.T) {
	// auth.mode == "" means no interceptor is registered. Any call
	// should succeed without a header.
	ts, hc := newAuthTestServer(t, server.AuthConfig{})
	client := talonv1connect.NewInfraServiceClient(hc, ts.URL)

	resp, err := client.NodeList(context.Background(), connect.NewRequest(&talonv1.Empty{}))
	if err != nil {
		t.Fatalf("NodeList with no auth: %v", err)
	}
	if resp.Msg == nil {
		t.Fatal("nil response")
	}
}

func TestAuthInterceptor_TokenMode_MissingHeader(t *testing.T) {
	ts, hc := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: "secret"})
	client := talonv1connect.NewInfraServiceClient(hc, ts.URL)

	_, err := client.NodeList(context.Background(), connect.NewRequest(&talonv1.Empty{}))
	if err == nil {
		t.Fatal("expected unauth error, got nil")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected *connect.Error, got %T", err)
	}
	if ce.Code() != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want CodeUnauthenticated", ce.Code())
	}
}

func TestAuthInterceptor_TokenMode_WrongToken(t *testing.T) {
	ts, hc := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: "secret"})
	client := talonv1connect.NewInfraServiceClient(hc, ts.URL)

	req := connect.NewRequest(&talonv1.Empty{})
	req.Header().Set("Authorization", "Bearer not-the-token")
	_, err := client.NodeList(context.Background(), req)
	if err == nil {
		t.Fatal("expected unauth error, got nil")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnauthenticated {
		t.Errorf("got %v, want CodeUnauthenticated", err)
	}
}

func TestAuthInterceptor_TokenMode_ValidToken(t *testing.T) {
	ts, hc := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: "secret"})
	client := talonv1connect.NewInfraServiceClient(hc, ts.URL)

	req := connect.NewRequest(&talonv1.Empty{})
	req.Header().Set("Authorization", "Bearer secret")
	resp, err := client.NodeList(context.Background(), req)
	if err != nil {
		t.Fatalf("NodeList with valid token: %v", err)
	}
	if resp.Msg == nil {
		t.Fatal("nil response")
	}
}

// Health is exempt — probes don't carry credentials. Verify that
// even with auth.mode == "token", calling Health without a header
// succeeds.
func TestAuthInterceptor_HealthExempt(t *testing.T) {
	ts, hc := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: "secret"})
	client := talonv1connect.NewInfraServiceClient(hc, ts.URL)

	resp, err := client.Health(context.Background(), connect.NewRequest(&talonv1.Empty{}))
	if err != nil {
		t.Fatalf("Health is supposed to be exempt: %v", err)
	}
	if !resp.Msg.Ok {
		t.Error("Health response should be ok=true")
	}
}

// Bearer prefix parsing edge cases — case-insensitive prefix,
// empty token, missing prefix all fall through to the same
// "missing token" rejection (AuthConfig.Authorize handles it).
func TestBearerToken(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOk bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"BEARER  spaced ", "spaced", true},
		{"abc", "", false},
		{"Bearer ", "", false},
		{"", "", false},
		{"Basic dXNlcjpwYXNz", "", false},
	}
	for _, c := range cases {
		got, ok := bearerToken(c.in)
		if got != c.want || ok != c.wantOk {
			t.Errorf("bearerToken(%q) = (%q, %v), want (%q, %v)",
				c.in, got, ok, c.want, c.wantOk)
		}
	}
}

// Raw HTTP probe — Health should return 200 with no auth header
// even when token mode is on. Belt-and-suspenders check that
// nothing in the Connect handler short-circuits before our
// interceptor's exemption fires.
func TestAuthInterceptor_HealthExempt_RawHTTP(t *testing.T) {
	ts, _ := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: "secret"})

	resp, err := http.Post(
		ts.URL+"/talon.v1.InfraService/Health",
		"application/json",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatalf("POST Health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("Health status = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
}
