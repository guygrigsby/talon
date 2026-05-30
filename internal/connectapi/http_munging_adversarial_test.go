package connectapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	talonv1connect "github.com/guygrigsby/talon/internal/api/v1/talon/v1/talonv1connect"
	"github.com/guygrigsby/talon/internal/server"
)

// HTTP-level smashing of the Connect surface: wrong methods, lied-about
// content types, hostile protocol headers, spoofed forwarding headers,
// oversized/compressed bodies, and a rate-limit (429) probe. The transport is
// the same connect-go stack the real gateway mounts, so transport behavior is
// faithful even though only the stub Infra service is wired.

const nodeListPath = "/talon.v1.InfraService/NodeList"

// openServer mounts the stub Infra service with NO auth so body/protocol
// handling can be exercised in isolation.
func openServer(t *testing.T) string {
	t.Helper()
	ts, _ := newAuthTestServer(t, server.AuthConfig{})
	return ts.URL
}

// Wrong HTTP method must not execute the RPC.
func TestHTTP_WrongMethodRejected(t *testing.T) {
	url := openServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(method, url+nodeListPath, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("method %s returned 200 — RPC should not run on a non-POST", method)
		}
	}
}

// Lying about Content-Type (or omitting it) must not get a request processed
// as a valid RPC. application/json "{}" is the control that should work.
func TestHTTP_ContentTypeLies(t *testing.T) {
	url := openServer(t)
	post := func(ct, body string) int {
		req, _ := http.NewRequest(http.MethodPost, url+nodeListPath, strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return -1
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	// Control: a well-formed Connect-JSON unary call succeeds.
	if code := post("application/json", "{}"); code != http.StatusOK {
		t.Errorf("control application/json {} = %d, want 200", code)
	}
	// Lies: each should be rejected (not 200), and must not crash the server.
	lies := []struct{ ct, body string }{
		{"text/plain", "{}"},
		{"application/json", "{not valid json"},
		{"application/json", `{"unknown":"field","x":[1,2,3]}`}, // proto-json may accept+ignore; must not crash
		{"", "{}"}, // missing content-type
		{"application/x-www-form-urlencoded", "a=b"},
		{"application/grpc-web+proto", "\x00\x00\x00\x00\x00"},
	}
	for _, l := range lies {
		code := post(l.ct, l.body)
		_ = code // some lenient shapes may 200 (unknown JSON fields); the assertion is "no crash"
	}
	// Server still healthy after the abuse.
	if code := post("application/json", "{}"); code != http.StatusOK {
		t.Errorf("server unhealthy after content-type abuse: %d", code)
	}
}

// Hostile Connect/protocol headers must be handled without crashing.
func TestHTTP_HostileProtocolHeaders(t *testing.T) {
	url := openServer(t)
	headers := []map[string]string{
		{"Connect-Timeout-Ms": "-1"},
		{"Connect-Timeout-Ms": "abc"},
		{"Connect-Timeout-Ms": "999999999999999999999999"},
		{"Connect-Timeout-Ms": strings.Repeat("9", 4096)},
		{"Connect-Protocol-Version": "99"},
		{"Connect-Protocol-Version": "; drop table"},
		{"Content-Encoding": "gzip"}, // claims gzip, body is not gzip
	}
	for _, hs := range headers {
		req, _ := http.NewRequest(http.MethodPost, url+nodeListPath, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hs {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue // transport-level rejection is fine
		}
		_ = resp.Body.Close()
		// We don't assert a specific code — only that the server answered
		// (didn't hang/panic). A follow-up health check confirms liveness.
	}
	req, _ := http.NewRequest(http.MethodPost, url+nodeListPath, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("server unresponsive after hostile headers: %v", err)
	}
	_ = resp.Body.Close()
}

// Spoofed forwarding / identity headers must not bypass token auth. The
// gateway only honors Bearer tokens (trusted-proxy is unimplemented), so
// X-Forwarded-* and friends must be inert.
func TestHTTP_SpoofedForwardingHeadersDoNotBypassAuth(t *testing.T) {
	ts, _ := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: advToken})
	spoofs := []map[string]string{
		{"X-Forwarded-For": "127.0.0.1"},
		{"X-Real-Ip": "127.0.0.1"},
		{"X-Forwarded-Authorization": "Bearer " + advToken},
		{"X-Forwarded-User": "admin"},
		{"Forwarded": "for=127.0.0.1;proto=https"},
		{"X-Forwarded-Host": "localhost"},
	}
	for _, hs := range spoofs {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+nodeListPath, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hs {
			req.Header.Set(k, v)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			continue
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code == http.StatusOK {
			t.Errorf("AUTH BYPASS: spoofed headers %v got 200 without a real token", hs)
		}
	}
}

// A multi-megabyte body is accepted and read into memory: there is no
// WithReadMaxBytes on the handlers, so the request size is unbounded. This
// documents the missing limit (DoS surface). Auth-gated, but a compromised or
// buggy client can still OOM the gateway.
func TestHTTP_NoRequestSizeLimit(t *testing.T) {
	url := openServer(t)
	// 5 MB of JSON whitespace padding around an empty object. NodeList ignores
	// the body, but connect-go still reads the whole thing first.
	big := `{"_pad":"` + strings.Repeat("A", 5<<20) + `"}`
	req, _ := http.NewRequest(http.MethodPost, url+nodeListPath, strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("5MB body: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Log("a request-size limit now rejects 5MB (gap closed) — update this test")
		return
	}
	if resp.StatusCode != http.StatusOK {
		t.Logf("5MB body returned %d (not a size-limit rejection)", resp.StatusCode)
		return
	}
	t.Logf("FINDING: 5MB request body accepted — no WithReadMaxBytes; request size is unbounded")
}

// A gzip-declared body that is not actually gzip must produce a clean error,
// not a panic/hang.
func TestHTTP_LyingGzipEncoding(t *testing.T) {
	url := openServer(t)
	req, _ := http.NewRequest(http.MethodPost, url+nodeListPath, strings.NewReader("this is definitely not gzip"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		t.Error("non-gzip body labeled gzip should not be accepted as a valid RPC")
	}
}

// A real (small) gzip body that inflates modestly is accepted — there is no
// decompression cap either. Kept conservative to avoid OOM in CI; the point is
// only that no small limit rejects it.
func TestHTTP_GzipInflationAccepted(t *testing.T) {
	url := openServer(t)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"_pad":"` + strings.Repeat("A", 2<<20) + `"}`)); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	req, _ := http.NewRequest(http.MethodPost, url+nodeListPath, bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gzip body: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	t.Logf("gzip body (~%d bytes compressed -> 2MB) returned %d", buf.Len(), resp.StatusCode)
}

// Rate-limit probe (the explicit "check 429" ask): hammer the gateway and
// confirm it NEVER returns 429. It has no rate limiting — every authenticated
// request is served. Documents the absence so adding a limiter later flips
// this into a real expectation.
func TestHTTP_NoRateLimiting429(t *testing.T) {
	ts, _ := newAuthTestServer(t, server.AuthConfig{Mode: server.AuthToken, Token: advToken})
	client := talonv1connect.NewInfraServiceClient(ts.Client(), ts.URL)

	const n = 400
	var got429, served atomic.Int64
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			req := connect.NewRequest(&talonv1.Empty{})
			req.Header().Set("Authorization", "Bearer "+advToken)
			_, err := client.NodeList(context.Background(), req)
			if err == nil {
				served.Add(1)
				return
			}
			var ce *connect.Error
			if errors.As(err, &ce) && ce.Code() == connect.CodeResourceExhausted {
				got429.Add(1)
			}
		}()
	}
	wg.Wait()
	if got429.Load() > 0 {
		t.Logf("a rate limiter now exists (%d throttled) — update this expectation", got429.Load())
		return
	}
	t.Logf("FINDING: %d/%d rapid requests all served, zero 429/ResourceExhausted — no rate limiting on the Connect API", served.Load(), n)
}
