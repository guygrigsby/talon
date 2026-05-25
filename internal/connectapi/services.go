package connectapi

import (
	"context"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	"github.com/guygrigsby/talon/internal/server"
)

// Every service struct here holds a single dependency: the
// server.Registry that already routes the WS path. Each method
// is a thin adapter — see bridge.go for the dispatch helpers.
//
// Layout: one struct per .proto service. New services land here
// when they're added to proto/talon/v1/*.

// ---- InfraService ---------------------------------------------------------

type InfraService struct {
	// Srv supplies uptime + version directly. Bypasses the
	// registry's "health" handler because that one panics when
	// hc.Session is nil — Connect calls don't have a Session.
	// Doing the read directly here is also more honest: health
	// shouldn't depend on per-connection state.
	Srv *server.Server
	Reg *server.Registry
}

func (s *InfraService) Health(ctx context.Context, req *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.HealthResponse], error) {
	return connect.NewResponse(&talonv1.HealthResponse{
		Ok:       true,
		Server:   "talon-gateway",
		UptimeMs: s.Srv.UptimeMs(),
		Version:  s.Srv.Version(),
	}), nil
}

func (s *InfraService) NodeList(ctx context.Context, req *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.NodeListResponse], error) {
	var out talonv1.NodeListResponse
	if err := dispatchInto(ctx, s.Reg, "node.list", nil, &out); err != nil {
		return nil, err
	}
	return connect.NewResponse(&out), nil
}

// ---- ConfigService --------------------------------------------------------

type ConfigService struct {
	Reg *server.Registry
}

func (s *ConfigService) Get(ctx context.Context, req *connect.Request[talonv1.ConfigGetRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "config.get", map[string]any{"path": req.Msg.GetPath()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *ConfigService) Set(ctx context.Context, req *connect.Request[talonv1.ConfigSetRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "config.set", map[string]any{
		"path":      req.Msg.GetPath(),
		"valueJson": req.Msg.GetValueJson(),
		"merge":     req.Msg.GetMerge(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *ConfigService) Schema(ctx context.Context, req *connect.Request[talonv1.ConfigSchemaRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	params := map[string]any{}
	if sec := req.Msg.GetSection(); sec != "" {
		params["section"] = sec
	}
	raw, err := dispatchJSON(ctx, s.Reg, "config.schema", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

// ---- AgentsService --------------------------------------------------------

type AgentsService struct {
	Reg *server.Registry
}

func (s *AgentsService) List(ctx context.Context, req *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "agents.list", nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *AgentsService) FilesList(ctx context.Context, req *connect.Request[talonv1.AgentFilesListRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "agents.files.list", map[string]any{"agentId": req.Msg.GetAgentId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *AgentsService) FilesGet(ctx context.Context, req *connect.Request[talonv1.AgentFilesGetRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "agents.files.get", map[string]any{
		"agentId": req.Msg.GetAgentId(),
		"file":    req.Msg.GetFile(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *AgentsService) FilesSet(ctx context.Context, req *connect.Request[talonv1.AgentFilesSetRequest]) (*connect.Response[talonv1.Empty], error) {
	_, err := dispatchJSON(ctx, s.Reg, "agents.files.set", map[string]any{
		"agentId": req.Msg.GetAgentId(),
		"file":    req.Msg.GetFile(),
		"content": req.Msg.GetContent(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.Empty{}), nil
}

func (s *AgentsService) IdentityGet(ctx context.Context, req *connect.Request[talonv1.AgentIdentityGetRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "agent.identity.get", map[string]any{"sessionKey": req.Msg.GetSessionKey()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *AgentsService) SkillsStatus(ctx context.Context, req *connect.Request[talonv1.SkillsStatusRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "skills.status", map[string]any{"agentId": req.Msg.GetAgentId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *AgentsService) MemoryAppend(ctx context.Context, req *connect.Request[talonv1.MemoryAppendRequest]) (*connect.Response[talonv1.Empty], error) {
	_, err := dispatchJSON(ctx, s.Reg, "memory.append", map[string]any{
		"agentId": req.Msg.GetAgentId(),
		"kind":    req.Msg.GetKind(),
		"text":    req.Msg.GetText(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.Empty{}), nil
}

// ---- ModelsService --------------------------------------------------------

type ModelsService struct {
	Reg *server.Registry
}

func (s *ModelsService) List(ctx context.Context, req *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "models.list", nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *ModelsService) AuthStatus(ctx context.Context, req *connect.Request[talonv1.ModelsAuthStatusRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	params := map[string]any{}
	if v := req.Msg.GetAgentId(); v != "" {
		params["agentId"] = v
	}
	if v := req.Msg.GetProvider(); v != "" {
		params["provider"] = v
	}
	if req.Msg.GetRefresh() {
		params["refresh"] = true
	}
	raw, err := dispatchJSON(ctx, s.Reg, "models.authStatus", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

// ---- PluginsService -------------------------------------------------------

type PluginsService struct {
	Reg *server.Registry
}

func (s *PluginsService) DepsStatus(ctx context.Context, req *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "plugins.deps.status", nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *PluginsService) DepsInstall(ctx context.Context, req *connect.Request[talonv1.DepsInstallRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "plugins.deps.install", map[string]any{"entry": req.Msg.GetEntry()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *PluginsService) DepsUninstall(ctx context.Context, req *connect.Request[talonv1.DepsUninstallRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "plugins.deps.uninstall", map[string]any{"entry": req.Msg.GetEntry()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *PluginsService) DepsDetail(ctx context.Context, req *connect.Request[talonv1.DepsDetailRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "plugins.deps.detail", map[string]any{"entry": req.Msg.GetEntry()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *PluginsService) CommandsList(ctx context.Context, req *connect.Request[talonv1.CommandsListRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	params := map[string]any{}
	if v := req.Msg.GetAgentId(); v != "" {
		params["agentId"] = v
	}
	if v := req.Msg.GetProvider(); v != "" {
		params["provider"] = v
	}
	if v := req.Msg.GetScope(); v != "" {
		params["scope"] = v
	}
	if req.Msg.GetIncludeArgs() {
		params["includeArgs"] = true
	}
	raw, err := dispatchJSON(ctx, s.Reg, "commands.list", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

// ---- CronService ----------------------------------------------------------

type CronService struct {
	Reg *server.Registry
}

func (s *CronService) List(ctx context.Context, req *connect.Request[talonv1.CronListRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	params := map[string]any{}
	if req.Msg.GetIncludeDisabled() {
		params["includeDisabled"] = true
	}
	raw, err := dispatchJSON(ctx, s.Reg, "cron.list", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *CronService) Add(ctx context.Context, req *connect.Request[talonv1.JSONPayload]) (*connect.Response[talonv1.JSONPayload], error) {
	// Pass JSON straight through — Add's payload shape is the
	// most variable (different job kinds, different schedules).
	var params any
	if err := jsonUnmarshalAny(req.Msg.GetJson(), &params); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	raw, err := dispatchJSON(ctx, s.Reg, "cron.add", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *CronService) Remove(ctx context.Context, req *connect.Request[talonv1.CronJobIDRequest]) (*connect.Response[talonv1.Empty], error) {
	_, err := dispatchJSON(ctx, s.Reg, "cron.remove", map[string]any{"id": req.Msg.GetId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.Empty{}), nil
}

func (s *CronService) Run(ctx context.Context, req *connect.Request[talonv1.CronJobIDRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "cron.run", map[string]any{"id": req.Msg.GetId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *CronService) Status(ctx context.Context, req *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "cron.status", nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *CronService) Show(ctx context.Context, req *connect.Request[talonv1.CronJobIDRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "cron.show", map[string]any{"id": req.Msg.GetId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *CronService) Enable(ctx context.Context, req *connect.Request[talonv1.CronJobIDRequest]) (*connect.Response[talonv1.Empty], error) {
	_, err := dispatchJSON(ctx, s.Reg, "cron.enable", map[string]any{"id": req.Msg.GetId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.Empty{}), nil
}

func (s *CronService) Disable(ctx context.Context, req *connect.Request[talonv1.CronJobIDRequest]) (*connect.Response[talonv1.Empty], error) {
	_, err := dispatchJSON(ctx, s.Reg, "cron.disable", map[string]any{"id": req.Msg.GetId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.Empty{}), nil
}

func (s *CronService) Runs(ctx context.Context, req *connect.Request[talonv1.CronRunsRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	params := map[string]any{"id": req.Msg.GetId()}
	if limit := req.Msg.GetLimit(); limit > 0 {
		params["limit"] = limit
	}
	raw, err := dispatchJSON(ctx, s.Reg, "cron.runs", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

// ---- ChannelsService ------------------------------------------------------

type ChannelsService struct {
	Reg *server.Registry
}

func (s *ChannelsService) TelegramVerify(ctx context.Context, req *connect.Request[talonv1.TelegramVerifyRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "channels.telegram.verify", map[string]any{"botToken": req.Msg.GetBotToken()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *ChannelsService) TelegramCaptureSender(ctx context.Context, req *connect.Request[talonv1.TelegramCaptureSenderRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	params := map[string]any{"botToken": req.Msg.GetBotToken()}
	if ms := req.Msg.GetTimeoutMs(); ms > 0 {
		params["timeoutMs"] = ms
	}
	raw, err := dispatchJSON(ctx, s.Reg, "channels.telegram.captureSender", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *ChannelsService) TelegramPersist(ctx context.Context, req *connect.Request[talonv1.TelegramPersistRequest]) (*connect.Response[talonv1.Empty], error) {
	_, err := dispatchJSON(ctx, s.Reg, "channels.telegram.persist", map[string]any{
		"agentId":  req.Msg.GetAgentId(),
		"botToken": req.Msg.GetBotToken(),
		"chatId":   req.Msg.GetChatId(),
		"confirm":  req.Msg.GetConfirm(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.Empty{}), nil
}
