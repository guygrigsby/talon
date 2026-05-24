package connectapi

import (
	"net/http"

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

	mux.Handle(talonv1connect.NewInfraServiceHandler(infra))
	mux.Handle(talonv1connect.NewConfigServiceHandler(cfg))
	mux.Handle(talonv1connect.NewAgentsServiceHandler(agents))
	mux.Handle(talonv1connect.NewModelsServiceHandler(models))
	mux.Handle(talonv1connect.NewSessionsServiceHandler(sessions))
	mux.Handle(talonv1connect.NewChatServiceHandler(chat))
	mux.Handle(talonv1connect.NewPluginsServiceHandler(plugins))
	mux.Handle(talonv1connect.NewCronServiceHandler(cron))
	mux.Handle(talonv1connect.NewChannelsServiceHandler(channels))
}
