package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Session struct {
	server *Server
	conn   *websocket.Conn
	connID string

	mu         sync.Mutex
	authed     bool
	role       string
	scopes     []string
	clientID   string
	instanceID string
	dedupKey   string // "<clientId>|<instanceId>" once handshake completes; empty otherwise

	// writeMu serializes WS writes. coder/websocket allows concurrent
	// Read+Write, but only one Write at a time — this matters once chat
	// streaming pushes events asynchronously alongside request replies.
	writeMu sync.Mutex
}

// AgentID returns the per-session agent identifier supplied at handshake
// (currently the connect.client.id). Reserved for future per-session state;
// returns "" when handshake has not completed.
func (s *Session) AgentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientID
}

// PushEvent writes a server-initiated event frame to this session. Safe for
// concurrent use across goroutines (write-serialized internally). A nil
// receiver returns errNoSession instead of panicking; tests that exercise
// chat.send synchronously without a real WS conn rely on this so the
// streaming goroutine can bail cleanly.
func (s *Session) PushEvent(ctx context.Context, event string, payload any) error {
	if s == nil || s.conn == nil {
		return errNoSession
	}
	return s.write(ctx, &Frame{Type: FrameEvent, Event: event, Payload: marshalRaw(payload)})
}

// shutdown closes the session's WS with a normal-closure status and the
// supplied reason. Used by Server.registerSession to evict an older
// session that's been replaced by a newer connect with the same client
// instanceId. Idempotent and safe on a nil session.
func (s *Session) shutdown(reason string) {
	if s == nil || s.conn == nil {
		return
	}
	_ = s.conn.Close(websocket.StatusNormalClosure, reason)
}

// register stores this session in the server's by-key map after a
// successful handshake. Empty clientID or instanceID skips registration —
// without both we can't tell two connections from the same tab apart from
// two tabs of the same UI.
func (s *Session) register() {
	s.mu.Lock()
	cid, iid := s.clientID, s.instanceID
	s.mu.Unlock()
	if cid == "" || iid == "" {
		return
	}
	key := cid + "|" + iid
	s.mu.Lock()
	s.dedupKey = key
	s.mu.Unlock()
	s.server.registerSession(key, s)
}

// deregister removes this session from the server's by-key map iff it's
// still the entry under our key. Called from Run's defer chain so it
// runs whether the session exits cleanly or via shutdown.
func (s *Session) deregister() {
	s.mu.Lock()
	key := s.dedupKey
	s.mu.Unlock()
	if key == "" {
		return
	}
	s.server.unregisterSession(key, s)
}

// errNoSession is returned by PushEvent when there is nothing to write to.
var errNoSession = errors.New("session is unavailable")

func newSession(s *Server, conn *websocket.Conn) *Session {
	id, _ := newConnID()
	return &Session{server: s, conn: conn, connID: id}
}

func (s *Session) Run(ctx context.Context) {
	defer s.conn.Close(websocket.StatusNormalClosure, "")
	defer s.deregister()

	if err := s.sendChallenge(ctx); err != nil {
		return
	}

	handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.handshake(handshakeCtx); err != nil {
		s.server.logf("handshake failed conn=%s: %v", s.connID, err)
		return
	}
	s.register()

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
	s.instanceID = p.Client.InstanceID
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
	start := time.Now()
	hc := HandlerCtx{Session: s}
	res, ferr := s.server.registry.Dispatch(ctx, hc, f.Method, f.Params)
	dur := time.Since(start).Truncate(time.Microsecond)
	// One line per RPC so the gateway logs become useful for
	// debugging "why isn't the UI tab working" without needing a
	// flag. Costs one map lookup + one log call per request; trivial
	// next to the WS write that follows.
	if ferr != nil {
		s.server.logf("rpc method=%s id=%s dur=%s err=%s msg=%s",
			f.Method, f.ID, dur, ferr.Code, ferr.Message)
		_ = s.replyError(ctx, f.ID, ferr)
		return
	}
	s.server.logf("rpc method=%s id=%s dur=%s ok",
		f.Method, f.ID, dur)
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
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
