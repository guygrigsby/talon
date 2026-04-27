package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Session struct {
	server *Server
	conn   *websocket.Conn
	connID string

	mu      sync.Mutex
	authed  bool
	role    string
	scopes  []string
	clientID string
}

func newSession(s *Server, conn *websocket.Conn) *Session {
	id, _ := newConnID()
	return &Session{server: s, conn: conn, connID: id}
}

func (s *Session) Run(ctx context.Context) {
	defer s.conn.Close(websocket.StatusNormalClosure, "")

	if err := s.sendChallenge(ctx); err != nil {
		return
	}

	handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.handshake(handshakeCtx); err != nil {
		s.server.logf("handshake failed conn=%s: %v", s.connID, err)
		return
	}

	for {
		var f Frame
		if err := s.read(ctx, &f); err != nil {
			return
		}
		if f.Type != FrameReq {
			continue
		}
		s.handleRequest(ctx, &f)
	}
}

func (s *Session) sendChallenge(ctx context.Context) error {
	nonce, _ := newConnID()
	return s.write(ctx, &Frame{
		Type:    FrameEvent,
		Event:   "connect.challenge",
		Payload: marshalRaw(ConnectChallenge{Nonce: nonce, Ts: time.Now().UnixMilli()}),
	})
}

func (s *Session) handshake(ctx context.Context) error {
	var f Frame
	if err := s.read(ctx, &f); err != nil {
		return err
	}
	if f.Type != FrameReq {
		return s.replyError(ctx, f.ID, &FrameError{Code: ErrCodeBadRequest, Message: "expected request frame"})
	}
	if f.Method != "connect" {
		return s.replyError(ctx, f.ID, &FrameError{Code: ErrCodeHandshakeRequired, Message: "first request must be connect"})
	}

	var p ConnectParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.replyError(ctx, f.ID, &FrameError{Code: ErrCodeBadRequest, Message: "invalid connect params: " + err.Error()})
	}
	if p.MinProtocol > ProtocolVersion || p.MaxProtocol < ProtocolVersion {
		return s.replyError(ctx, f.ID, &FrameError{
			Code:    ErrCodeProtocolMismatch,
			Message: fmt.Sprintf("server protocol %d not in client range [%d, %d]", ProtocolVersion, p.MinProtocol, p.MaxProtocol),
		})
	}

	auth, ferr := s.server.cfg.Auth.Authorize(&p)
	if ferr != nil {
		return s.replyError(ctx, f.ID, ferr)
	}

	s.mu.Lock()
	s.authed = true
	s.role = auth.Role
	s.scopes = auth.Scopes
	s.clientID = p.Client.ID
	s.mu.Unlock()

	hello := HelloOK{
		Type:     "hello-ok",
		Protocol: ProtocolVersion,
		Server:   ServerInfo{Version: serverVersion, ConnID: s.connID},
		Features: Features{
			Methods: s.server.registry.Methods(),
			Events:  []string{"connect.challenge"},
		},
		Snapshot: Snapshot{
			Presence:     []any{},
			Health:       map[string]any{"ok": true},
			StateVersion: StateVersion{Version: 0},
			UptimeMs:     s.server.uptimeMs(),
			AuthMode:     string(s.server.cfg.Auth.Mode),
		},
		Auth: &AuthInfo{
			Role:       auth.Role,
			Scopes:     auth.Scopes,
			IssuedAtMs: time.Now().UnixMilli(),
		},
		Policy: Policy{
			MaxPayload:       16 * 1024 * 1024,
			MaxBufferedBytes: 64 * 1024 * 1024,
			TickIntervalMs:   1000,
		},
	}
	return s.replyOK(ctx, f.ID, hello)
}

func (s *Session) handleRequest(ctx context.Context, f *Frame) {
	hc := HandlerCtx{Session: s}
	res, ferr := s.server.registry.Dispatch(ctx, hc, f.Method, f.Params)
	if ferr != nil {
		_ = s.replyError(ctx, f.ID, ferr)
		return
	}
	_ = s.replyOK(ctx, f.ID, res)
}

func (s *Session) replyOK(ctx context.Context, id string, payload any) error {
	ok := true
	return s.write(ctx, &Frame{Type: FrameRes, ID: id, OK: &ok, Payload: marshalRaw(payload)})
}

func (s *Session) replyError(ctx context.Context, id string, ferr *FrameError) error {
	ok := false
	return s.write(ctx, &Frame{Type: FrameRes, ID: id, OK: &ok, Error: ferr})
}

func (s *Session) read(ctx context.Context, f *Frame) error {
	_, data, err := s.conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, f)
}

func (s *Session) write(ctx context.Context, f *Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, b)
}

func marshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func newConnID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
