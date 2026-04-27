package server

import "encoding/json"

const (
	FrameReq   = "req"
	FrameRes   = "res"
	FrameEvent = "event"

	ProtocolVersion = 3
)

type Frame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	OK      *bool           `json:"ok,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *FrameError     `json:"error,omitempty"`
	Event   string          `json:"event,omitempty"`
	Seq     *int            `json:"seq,omitempty"`
}

type FrameError struct {
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Details     json.RawMessage `json:"details,omitempty"`
	Retryable   *bool           `json:"retryable,omitempty"`
	RetryAfter  *int            `json:"retryAfterMs,omitempty"`
}

const (
	ErrCodeBadRequest        = "BAD_REQUEST"
	ErrCodeUnauthorized      = "UNAUTHORIZED"
	ErrCodeProtocolMismatch  = "PROTOCOL_MISMATCH"
	ErrCodeMethodNotFound    = "METHOD_NOT_FOUND"
	ErrCodeInternal          = "INTERNAL"
	ErrCodeHandshakeRequired = "HANDSHAKE_REQUIRED"
)

type ConnectParams struct {
	MinProtocol int                    `json:"minProtocol"`
	MaxProtocol int                    `json:"maxProtocol"`
	Client      ConnectClient          `json:"client"`
	Caps        []string               `json:"caps,omitempty"`
	Role        string                 `json:"role,omitempty"`
	Scopes      []string               `json:"scopes,omitempty"`
	Auth        *ConnectAuth           `json:"auth,omitempty"`
	Locale      string                 `json:"locale,omitempty"`
	UserAgent   string                 `json:"userAgent,omitempty"`
	Permissions map[string]bool        `json:"permissions,omitempty"`
	Commands    []string               `json:"commands,omitempty"`
	Device      map[string]any         `json:"device,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

type ConnectClient struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	Platform    string `json:"platform"`
	Mode        string `json:"mode"`
	InstanceID  string `json:"instanceId,omitempty"`
}

type ConnectAuth struct {
	Token          string `json:"token,omitempty"`
	BootstrapToken string `json:"bootstrapToken,omitempty"`
	DeviceToken    string `json:"deviceToken,omitempty"`
	Password       string `json:"password,omitempty"`
}

type HelloOK struct {
	Type     string         `json:"type"`
	Protocol int            `json:"protocol"`
	Server   ServerInfo     `json:"server"`
	Features Features       `json:"features"`
	Snapshot Snapshot       `json:"snapshot"`
	Auth     *AuthInfo      `json:"auth,omitempty"`
	Policy   Policy         `json:"policy"`
}

type ServerInfo struct {
	Version string `json:"version"`
	ConnID  string `json:"connId"`
}

type Features struct {
	Methods []string `json:"methods"`
	Events  []string `json:"events"`
}

type Snapshot struct {
	Presence     []any        `json:"presence"`
	Health       any          `json:"health"`
	StateVersion StateVersion `json:"stateVersion"`
	UptimeMs     int64        `json:"uptimeMs"`
	AuthMode     string       `json:"authMode,omitempty"`
}

type StateVersion struct {
	Version int `json:"version"`
}

type AuthInfo struct {
	Role       string   `json:"role"`
	Scopes     []string `json:"scopes"`
	IssuedAtMs int64    `json:"issuedAtMs,omitempty"`
}

type Policy struct {
	MaxPayload       int `json:"maxPayload"`
	MaxBufferedBytes int `json:"maxBufferedBytes"`
	TickIntervalMs   int `json:"tickIntervalMs"`
}

type ConnectChallenge struct {
	Nonce string `json:"nonce"`
	Ts    int64  `json:"ts"`
}
