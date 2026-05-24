package connectapi

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/guygrigsby/talon/internal/server"
)

// authInterceptor enforces parity with the WS handshake's auth
// gate. WS does it once per connection in the `connect` frame and
// caches the result on the Session; Connect has no handshake, so
// every request gets authenticated independently from headers.
//
// The Bearer token is the only mechanism supported today because
// it's the only mode AuthConfig.Authorize implements end-to-end
// (password / trusted-proxy come back as ErrCodeInternal). When
// those land, parsing here will need to grow to cover them.
//
// Health and the gRPC reflection endpoint are intentionally NOT
// gated — they're discovery surfaces a browser/proxy hits before
// it has any credentials. Everything else requires a valid token
// when auth.mode != "none".
type authInterceptor struct {
	auth server.AuthConfig
}

// authCtxKey carries the validated AuthResult on the request
// context. Adapters that care about role/scopes (none today, but
// chat.send + future per-tool scope checks will) can pull it via
// AuthFromContext. Unexported key — only this package writes; only
// this package's accessor reads. Standard ctx-value hygiene.
type authCtxKey struct{}

// AuthFromContext returns the AuthResult installed by the
// interceptor, or the zero value if the call was unauthenticated
// (auth.mode == "none"). Callers should not depend on the zero
// value when mode != "none" — the interceptor would have failed
// the request before the handler ran.
func AuthFromContext(ctx context.Context) server.AuthResult {
	if v, ok := ctx.Value(authCtxKey{}).(server.AuthResult); ok {
		return v
	}
	return server.AuthResult{}
}

// authorize is the shared validation step. Extracts the Bearer
// token from the request header, hands it to server.AuthConfig
// (the same code the WS handshake uses), translates the resulting
// FrameError into a connect.Error. Returns the validated result
// for the caller to stash on the context.
func (a *authInterceptor) authorize(headerGet func(string) string) (server.AuthResult, error) {
	p := &server.ConnectParams{}
	if h := strings.TrimSpace(headerGet("Authorization")); h != "" {
		if tok, ok := bearerToken(h); ok {
			p.Auth = &server.ConnectAuth{Token: tok}
		}
	}
	res, fe := a.auth.Authorize(p)
	if fe != nil {
		return server.AuthResult{}, frameErrorToConnect("auth", fe)
	}
	return res, nil
}

// WrapUnary returns a connect.UnaryInterceptorFunc that validates
// auth before delegating to the wrapped handler. Failure here
// short-circuits with the proper connect.Code (401 for bad token,
// 500 if the server is misconfigured) before any handler logic
// runs.
func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		// Health is intentionally exempt. Browsers / load-balancers
		// hit it before they have any credentials; gating it would
		// break readiness probes.
		if isHealthProcedure(req.Spec().Procedure) {
			return next(ctx, req)
		}
		res, err := a.authorize(req.Header().Get)
		if err != nil {
			return nil, err
		}
		return next(context.WithValue(ctx, authCtxKey{}, res), req)
	}
}

// WrapStreamingHandler covers server-stream + bidi RPCs (today:
// ChatService.Subscribe, SessionsService.Subscribe). Same gate as
// unary, applied before the first stream message goes out.
func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if isHealthProcedure(conn.Spec().Procedure) {
			return next(ctx, conn)
		}
		res, err := a.authorize(conn.RequestHeader().Get)
		if err != nil {
			return err
		}
		return next(context.WithValue(ctx, authCtxKey{}, res), conn)
	}
}

// WrapStreamingClient is required by connect.Interceptor but the
// server-side embedding doesn't make client calls; it's a no-op.
// Defined to satisfy the interface so newAuthInterceptor returns
// a real connect.Interceptor.
func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// newAuthInterceptor returns nil when auth is disabled — there's
// no work to do and skipping registration shaves one ctx hop off
// every request. Otherwise returns an interceptor that gates
// every call.
func newAuthInterceptor(cfg server.AuthConfig) connect.Interceptor {
	if cfg.Mode == "" || cfg.Mode == server.AuthNone {
		return nil
	}
	return &authInterceptor{auth: cfg}
}

// bearerToken extracts the token from a Bearer Authorization
// header. Returns "", false if the header isn't a well-formed
// Bearer line — the caller treats that the same as a missing
// header (AuthConfig.Authorize rejects it on its own).
func bearerToken(h string) (string, bool) {
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// isHealthProcedure tells the interceptor to skip auth for the
// Infra.Health endpoint. Probes don't carry credentials and the
// payload is non-sensitive (uptime + version + ok). Match by
// suffix so the procedure prefix (/talon.v1.InfraService/) and the
// method name are both checked without hard-coding the full path.
func isHealthProcedure(procedure string) bool {
	return strings.HasSuffix(procedure, "/Health")
}
