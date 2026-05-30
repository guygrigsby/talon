package connectapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	talonv1connect "github.com/guygrigsby/talon/internal/api/v1/talon/v1/talonv1connect"
	"github.com/guygrigsby/talon/internal/server"
)

// Adversarial auth tests: throw hostile credentials / headers / modes at the
// interceptor and confirm it always fails closed and never panics. The happy
// paths live in auth_test.go; this file is the "try to break in" half.

const advToken = "s3cr3t-correct-horse"

func tokenInterceptor() *authInterceptor {
	return &authInterceptor{auth: server.AuthConfig{Mode: server.AuthToken, Token: advToken}}
}

// authorize() is the value-level gate, reachable without HTTP. Drive it with
// header values an HTTP client would refuse to send (NUL bytes, megabyte
// tokens) to confirm the parse + constant-time compare hold up.
func TestAuthorize_HostileTokenValues(t *testing.T) {
	ai := tokenInterceptor()
	cases := []struct {
		name      string
		header    string
		wantAuthd bool
	}{
		{"correct", "Bearer " + advToken, true},
		// bearerToken trims surrounding whitespace, so a stray trailing
		// newline still resolves to the exact token. Benign (you still need
		// the secret) but locked in so the trim behavior is intentional.
		{"correct trailing newline", "Bearer " + advToken + "\n", true},
		{"empty", "", false},
		{"scheme only", "Bearer", false},
		{"scheme space only", "Bearer ", false},
		{"wrong scheme", "Basic " + advToken, false},
		{"nul suffix", "Bearer " + advToken + "\x00", false},
		{"prefix of token", "Bearer " + advToken[:len(advToken)-1], false},
		{"token plus junk", "Bearer " + advToken + "x", false},
		{"tab instead of space", "Bearer\t" + advToken, false},
		{"megabyte token", "Bearer " + strings.Repeat("A", 1<<20), false},
		{"case-flipped token", "Bearer " + strings.ToUpper(advToken), false},
		{"unicode lookalike", "Bearer ѕ3cr3t-correct-horse", false}, // Cyrillic 'ѕ'
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ai.authorize(func(k string) string {
				if k == "Authorization" {
					return c.header
				}
				return ""
			})
			if c.wantAuthd && err != nil {
				t.Fatalf("expected authorized, got error: %v", err)
			}
			if !c.wantAuthd && err == nil {
				t.Fatalf("AUTH BYPASS: hostile header %q was accepted", c.header)
			}
		})
	}
}

// Auth modes that Authorize hasn't implemented (password, trusted-proxy) and
// any unknown mode must fail closed — never silently authorize. A regression
// here would turn a misconfiguration into an open gateway.
func TestAuthorize_UnimplementedModesFailClosed(t *testing.T) {
	for _, mode := range []server.AuthMode{server.AuthPassword, server.AuthTrustedProxy, "totally-made-up"} {
		t.Run(string(mode), func(t *testing.T) {
			ai := &authInterceptor{auth: server.AuthConfig{Mode: mode, Token: advToken}}
			// Even with the "correct" token present, an unimplemented mode
			// must not grant access.
			_, err := ai.authorize(func(string) string { return "Bearer " + advToken })
			if err == nil {
				t.Fatalf("FAIL-OPEN: mode %q authorized a request", mode)
			}
		})
	}
}

// token mode with no token configured is a server misconfiguration; it must
// reject every request rather than accept an empty token.
func TestAuthorize_TokenModeNoTokenConfigured(t *testing.T) {
	ai := &authInterceptor{auth: server.AuthConfig{Mode: server.AuthToken, Token: ""}}
	if _, err := ai.authorize(func(string) string { return "Bearer anything" }); err == nil {
		t.Fatal("FAIL-OPEN: token mode with empty configured token accepted a request")
	}
	if _, err := ai.authorize(func(string) string { return "Bearer " }); err == nil {
		t.Fatal("FAIL-OPEN: token mode with empty configured token accepted an empty token")
	}
}

// isHealthProcedure matches by suffix, so it exempts ANY procedure ending in
// "/Health" — not just InfraService/Health. This test documents that latent
// footgun: if a future service grows a Health RPC, it would be silently
// unauthenticated. Recommendation: match the exact Infra procedure instead.
func TestIsHealthProcedure_SuffixIsOverBroad(t *testing.T) {
	exempt := []string{
		"/talon.v1.InfraService/Health",
		"/some.evil.Service/Health",       // <-- over-broad: also exempt
		"/talon.v1.SecretsService/Health", // <-- would auto-exempt a future svc
	}
	for _, p := range exempt {
		if !isHealthProcedure(p) {
			t.Errorf("expected %q to be treated as health-exempt", p)
		}
	}
	gated := []string{
		"/talon.v1.InfraService/NodeList",
		"/talon.v1.InfraService/Healthy", // does not end in /Health
		"/talon.v1.InfraService/HealthCheck",
		"/Health", // no service prefix; harmless but note the bare match
	}
	for _, p := range gated {
		got := isHealthProcedure(p)
		// "/Health" technically ends in "/Health" -> true; assert the
		// genuinely-gated ones stay gated.
		if p != "/Health" && got {
			t.Errorf("expected %q to be gated, but it is exempt", p)
		}
	}
}

// Raw-HTTP header abuse: duplicate Authorization headers, junk schemes, and an
// oversized-but-legal token must never return 200, and the server must stay
// healthy for a subsequent legitimate call.
func TestAuthInterceptor_RawHTTP_HostileHeaders(t *testing.T) {
	ts, _ := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: advToken})
	url := ts.URL + "/talon.v1.InfraService/NodeList"

	hostile := []func(*http.Request){
		func(r *http.Request) {}, // no header at all
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
		func(r *http.Request) { r.Header.Set("Authorization", "Basic "+advToken) },
		func(r *http.Request) {
			// Duplicate headers: a smuggling attempt where one is valid.
			r.Header.Add("Authorization", "Bearer wrong")
			r.Header.Add("Authorization", "Bearer "+advToken)
		},
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 60_000)) },
	}
	for i, mod := range hostile {
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("case %d: build request: %v", i, err)
		}
		req.Header.Set("Content-Type", "application/json")
		mod(req)
		resp, err := ts.Client().Do(req)
		if err != nil {
			continue // transport refused it (e.g. invalid header) — that's a reject
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("AUTH BYPASS: hostile request %d returned 200", i)
		}
	}

	// Server still serves a legitimate request after the abuse.
	client := talonv1connect.NewInfraServiceClient(ts.Client(), ts.URL)
	req := connect.NewRequest(&talonv1.Empty{})
	req.Header().Set("Authorization", "Bearer "+advToken)
	if _, err := client.NodeList(context.Background(), req); err != nil {
		t.Fatalf("server unhealthy after hostile traffic: %v", err)
	}
}

// Hammer the gate concurrently with a mix of valid and invalid tokens. Run
// with `go test -race` to catch interceptor data races; without -race it still
// asserts each request gets the correct verdict under load.
func TestAuthInterceptor_ConcurrentMixedTokens(t *testing.T) {
	ts, _ := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: advToken})
	client := talonv1connect.NewInfraServiceClient(ts.Client(), ts.URL)

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			valid := i%2 == 0
			req := connect.NewRequest(&talonv1.Empty{})
			if valid {
				req.Header().Set("Authorization", "Bearer "+advToken)
			} else {
				req.Header().Set("Authorization", "Bearer nope-"+strings.Repeat("x", i))
			}
			_, err := client.NodeList(context.Background(), req)
			switch {
			case valid && err != nil:
				t.Errorf("valid token rejected: %v", err)
			case !valid && err == nil:
				t.Errorf("AUTH BYPASS under load: invalid token accepted")
			case !valid:
				var ce *connect.Error
				if !errors.As(err, &ce) || ce.Code() != connect.CodeUnauthenticated {
					t.Errorf("invalid token gave %v, want Unauthenticated", err)
				}
			}
		}(i)
	}
	wg.Wait()
}
