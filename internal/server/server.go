package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const serverVersion = "talon-gateway 0.1.0-dev"

type Config struct {
	Addr   string
	WebDir string
	Auth   AuthConfig
	Logger *log.Logger
}

type Server struct {
	cfg       Config
	mux       *http.ServeMux
	registry  *Registry
	startedAt time.Time
}

func New(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux(), registry: NewRegistry(), startedAt: time.Now()}
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

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}
