package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/guygrigsby/talon/internal/openclaw"
)

const serverVersion = "0.1.0-dev"

type Config struct {
	Addr   string
	WebDir string
	Auth   AuthConfig
	Logger *log.Logger

	// AgentResolver, if set, enables chat.send by translating sessionKey →
	// agent → ModelID. Leaving nil disables chat.send (returns INTERNAL).
	AgentResolver AgentResolver
	// ProviderFactory pairs with AgentResolver to materialize provider
	// implementations for the agent's primary model.
	ProviderFactory ProviderFactory
	// Paths, when set, enables the read-only RPCs sourced from the
	// merged config (agents.list, models.list, config.schema). Leaving
	// it as the zero value disables those handlers.
	Paths openclaw.Paths
	// WorkspaceResolver, paired with ToolRunnerFor, enables tool calling
	// in chat.send. Leaving either nil keeps chat in text-only mode.
	WorkspaceResolver WorkspaceResolver
	// ToolRunnerFor returns a per-agent ToolRunner given a workspace
	// directory. Called once per chat.send.
	ToolRunnerFor func(workspace string) ToolRunner
}

type Server struct {
	cfg       Config
	mux       *http.ServeMux
	registry  *Registry
	startedAt time.Time

	// sessions tracks active authenticated sessions keyed by
	// "<clientId>|<instanceId>" (only registered when both are
	// non-empty). When a new connect handshake completes with a key
	// already in the map, the older session is closed with a structured
	// reason. This guards against the openclaw web UI's habit of
	// occasionally opening two simultaneous WS connections from a single
	// page load (Lit re-mount, HMR, or a connectGateway race during
	// bootstrap).
	sessionsMu sync.Mutex
	sessions   map[string]*Session
}

func New(cfg Config) *Server {
	s := &Server{
		cfg:       cfg,
		mux:       http.NewServeMux(),
		registry:  NewRegistry(),
		startedAt: time.Now(),
		sessions:  make(map[string]*Session),
	}
	if cfg.AgentResolver != nil && cfg.ProviderFactory != nil {
		ch := NewChatHandler(cfg.AgentResolver, cfg.ProviderFactory, NewChatStore())
		if cfg.WorkspaceResolver != nil && cfg.ToolRunnerFor != nil {
			ch = ch.WithTools(cfg.WorkspaceResolver, cfg.ToolRunnerFor)
		}
		ch.Register(s.registry)
	}
	if cfg.Paths.Talon.Dir != "" {
		NewReadHandler(cfg.Paths).Register(s.registry)
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/ws", s.handleWS)

	var staticHandler http.Handler
	if s.cfg.WebDir != "" {
		staticHandler = http.FileServer(http.Dir(s.cfg.WebDir))
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			s.handleWS(w, r)
			return
		}
		if staticHandler != nil {
			staticHandler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (s *Server) Run(ctx context.Context) error {
	hs := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- hs.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"service":  "talon-gateway",
		"version":  serverVersion,
		"uptimeMs": s.uptimeMs(),
		"authMode": string(s.cfg.Auth.Mode),
	})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 * 1024 * 1024)

	sess := newSession(s, conn)
	s.logf("ws connected conn=%s remote=%s", sess.connID, r.RemoteAddr)
	sess.Run(r.Context())
	s.logf("ws closed conn=%s", sess.connID)
}

func (s *Server) uptimeMs() int64 {
	return time.Since(s.startedAt).Milliseconds()
}

// registerSession swaps sess into the sessions map under key. If a prior
// session was registered there, it's closed with a structured reason — the
// caller (Session.handshake) does this immediately after hello-ok so the
// stale half of a duplicate-connect race exits cleanly.
//
// Returns the displaced session (nil if none) so the caller can wait for
// it to drain if needed.
func (s *Server) registerSession(key string, sess *Session) *Session {
	if key == "" {
		return nil
	}
	s.sessionsMu.Lock()
	old := s.sessions[key]
	s.sessions[key] = sess
	s.sessionsMu.Unlock()
	if old != nil && old != sess {
		s.logf("ws replaced conn=%s by conn=%s key=%q", old.connID, sess.connID, key)
		old.shutdown("replaced-by-newer-instance")
	}
	return old
}

// unregisterSession removes sess from the map iff it's still the active
// entry under key. Compare-and-delete avoids the case where the
// just-displaced session deregisters its successor on the way out.
func (s *Server) unregisterSession(key string, sess *Session) {
	if key == "" {
		return
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if s.sessions[key] == sess {
		delete(s.sessions, key)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}
