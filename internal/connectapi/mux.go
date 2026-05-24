package connectapi

import (
	"net/http"

	"connectrpc.com/connect"

	talonv1connect "github.com/guygrigsby/talon/internal/api/v1/talon/v1/talonv1connect"
	"github.com/guygrigsby/talon/internal/server"
)

// Register attaches every talon.v1.* Connect service to mux.
// Routes look like /talon.v1.InfraService/Health — they coexist
// with the existing /ws (WebSocket) and /healthz routes without
// collision because Connect uses HTTP POST under the
// /talon.v1.* prefix.
//
// One call wires everything; per-service Register helpers stay
// private. This keeps the call site (server.go's routes()) clean.
//
// srv is needed by InfraService for Health (uptime + version
// come from server state, not the registry, because the legacy
// registry handler reads them via hc.Session — a path Connect
// calls don't have).
func Register(mux *http.ServeMux, srv *server.Server) {
	reg := srv.Registry()
	infra := &InfraService{Srv: srv, Reg: reg}
	cfg := &ConfigService{Reg: reg}
	agents := &AgentsService{Reg: reg}
	models := &ModelsService{Reg: reg}
	sessions := &SessionsService{Reg: reg}
	chat := &ChatService{Reg: reg}
	plugins := &PluginsService{Reg: reg}
	cron := &CronService{Reg: reg}
	channels := &ChannelsService{Reg: reg}

	// Interceptors mirror the WS handshake's pre-dispatch checks
	// (auth today; logging / rate-limit / scope when those land).
	// Returning nil from newAuthInterceptor when auth.mode == "none"
	// keeps the slice empty in that case so callers don't pay for
	// a no-op hop.
	var opts []connect.HandlerOption
	if ai := newAuthInterceptor(srv.Auth()); ai != nil {
		opts = append(opts, connect.WithInterceptors(ai))
	}

	mux.Handle(talonv1connect.NewInfraServiceHandler(infra, opts...))
	mux.Handle(talonv1connect.NewConfigServiceHandler(cfg, opts...))
	mux.Handle(talonv1connect.NewAgentsServiceHandler(agents, opts...))
	mux.Handle(talonv1connect.NewModelsServiceHandler(models, opts...))
	mux.Handle(talonv1connect.NewSessionsServiceHandler(sessions, opts...))
	mux.Handle(talonv1connect.NewChatServiceHandler(chat, opts...))
	mux.Handle(talonv1connect.NewPluginsServiceHandler(plugins, opts...))
	mux.Handle(talonv1connect.NewCronServiceHandler(cron, opts...))
	mux.Handle(talonv1connect.NewChannelsServiceHandler(channels, opts...))
}
