// Package-internal error envelope + codes shared by the Connect
// bridge and the auth interceptor. The WS frame types (Frame,
// FrameReq/Res/Event, ProtocolVersion, HelloOK, ...) were stripped
// with the rest of the WS surface — Connect handles framing now.
//
// ConnectParams / ConnectAuth survive in trimmed form because
// AuthConfig.Authorize takes one as input and the Connect auth
// interceptor builds one from the Authorization header.

package server

import "encoding/json"

// FrameError is the registry-level error envelope. Handlers return
// it from inside the *FrameError result of a Handler; the Connect
// bridge translates it into a connect.Error with the appropriate
// status code.
type FrameError struct {
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	Details    json.RawMessage `json:"details,omitempty"`
	Retryable  *bool           `json:"retryable,omitempty"`
	RetryAfter *int            `json:"retryAfterMs,omitempty"`
}

// Error codes used in FrameError.Code. The Connect bridge maps
// these to connect.Code{InvalidArgument, Unauthenticated, NotFound,
// Internal}; anything else falls through to Unknown.
const (
	ErrCodeBadRequest     = "BAD_REQUEST"
	ErrCodeUnauthorized   = "UNAUTHORIZED"
	ErrCodeMethodNotFound = "METHOD_NOT_FOUND"
	ErrCodeInternal       = "INTERNAL"
)

// ConnectParams is the input shape for AuthConfig.Authorize. The
// WS handshake used to populate every field (client, caps, role,
// scopes, ...); the Connect auth interceptor only fills Auth.
// Other fields are kept on the struct so any future caller (CLI
// dispatch, plugin RPC) can populate them without an Authorize
// signature change.
type ConnectParams struct {
	Role   string       `json:"role,omitempty"`
	Scopes []string     `json:"scopes,omitempty"`
	Auth   *ConnectAuth `json:"auth,omitempty"`
}

// ConnectAuth carries the credentials Authorize consumes. Only
// Token is used today; the password / bootstrap / device-token
// fields are placeholders for the modes Authorize doesn't yet
// implement.
type ConnectAuth struct {
	Token          string `json:"token,omitempty"`
	BootstrapToken string `json:"bootstrapToken,omitempty"`
	DeviceToken    string `json:"deviceToken,omitempty"`
	Password       string `json:"password,omitempty"`
}
