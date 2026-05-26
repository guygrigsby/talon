package server

import (
	"context"
	"encoding/json"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/memory"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// talonpath.Paths is referenced in NewHostService; keep the import explicit
// even if Go tooling otherwise warns about disuse in tests where only the
// constructor is exercised.
var _ talonpath.Paths

// HostService implements pb.HostServer — the back-channel surface
// plugins call into talon. Each method maps to the same in-process
// state the WS gateway's read/chat/memory handlers serve, so plugins
// see the same view the UI does. Methods are individually
// capability-gated by the plugin host's UnaryInterceptor; this type
// implements only the business logic.
//
// The methods deliberately reuse existing handler logic rather than
// duplicate it — drift between the WS view and the plugin view of the
// same state would be a nightmare to debug. When the WS handler returns
// a typed map[string]any, we json.Marshal it into the protobuf's
// raw_json field; this trades type-safety inside protobuf for
// canonical-shape parity with what the UI sees.
type HostService struct {
	pb.UnimplementedHostServer

	paths     talonpath.Paths
	reads     *ReadHandler
	chat      *ChatHandler // may be nil when chat is not configured
	chatStore *ChatStore
	sessions  *SessionStore
}

// NewHostService constructs the back-channel service from already-
// running talon state. reads, chatStore, and sessions are required;
// chat may be nil (Host.RunSubagent will then return Unimplemented).
func NewHostService(paths talonpath.Paths, reads *ReadHandler, chat *ChatHandler, chatStore *ChatStore, sessions *SessionStore) *HostService {
	return &HostService{
		paths:     paths,
		reads:     reads,
		chat:      chat,
		chatStore: chatStore,
		sessions:  sessions,
	}
}

// rawFromAny marshals an arbitrary handler return into JSON for the
// raw_json proto fields. Tests rely on this producing the same shape
// the WS side returns for the same operation.
func rawFromAny(v any) ([]byte, error) {
	return json.Marshal(v)
}

func internalErrf(err error) error {
	return status.Error(codes.Internal, err.Error())
}

func frameErr(ferr *FrameError) error {
	if ferr == nil {
		return nil
	}
	switch ferr.Code {
	case ErrCodeBadRequest:
		return status.Error(codes.InvalidArgument, ferr.Message)
	case ErrCodeUnauthorized:
		return status.Error(codes.Unauthenticated, ferr.Message)
	case ErrCodeMethodNotFound:
		return status.Error(codes.Unimplemented, ferr.Message)
	default:
		return status.Error(codes.Internal, ferr.Message)
	}
}

// =============================================================================
// Reads
// =============================================================================

func (s *HostService) GetConfig(ctx context.Context, req *pb.GetConfigRequest) (*pb.GetConfigResponse, error) {
	if s.reads == nil {
		return nil, status.Error(codes.Unimplemented, "reads not configured")
	}
	res, ferr := s.reads.handleConfigGet(ctx, HandlerCtx{}, nil)
	if ferr != nil {
		return nil, frameErr(ferr)
	}
	envelope := res.(map[string]any)
	cfg := envelope["config"]
	raw, err := rawFromAny(cfg)
	if err != nil {
		return nil, internalErrf(err)
	}
	if path := req.GetPath(); path != "" {
		// Scoped lookup. Returning JSON null for not-found is consistent
		// with how the WS side treats absent paths.
		v := gjson.GetBytes(raw, path)
		if !v.Exists() {
			return &pb.GetConfigResponse{RawJson: []byte("null")}, nil
		}
		return &pb.GetConfigResponse{RawJson: []byte(v.Raw)}, nil
	}
	return &pb.GetConfigResponse{RawJson: raw}, nil
}

func (s *HostService) ListAgents(ctx context.Context, _ *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	if s.reads == nil {
		return nil, status.Error(codes.Unimplemented, "reads not configured")
	}
	res, ferr := s.reads.handleAgentsList(ctx, HandlerCtx{}, nil)
	if ferr != nil {
		return nil, frameErr(ferr)
	}
	raw, err := rawFromAny(res)
	if err != nil {
		return nil, internalErrf(err)
	}
	return &pb.ListAgentsResponse{RawJson: raw}, nil
}

func (s *HostService) GetAgentIdentity(ctx context.Context, req *pb.GetAgentIdentityRequest) (*pb.GetAgentIdentityResponse, error) {
	if s.reads == nil {
		return nil, status.Error(codes.Unimplemented, "reads not configured")
	}
	params, _ := json.Marshal(map[string]any{"agentId": req.GetAgentId()})
	res, ferr := s.reads.handleAgentIdentityGet(ctx, HandlerCtx{}, params)
	if ferr != nil {
		return nil, frameErr(ferr)
	}
	if res == nil {
		return &pb.GetAgentIdentityResponse{AgentId: req.GetAgentId()}, nil
	}
	m := res.(map[string]any)
	return &pb.GetAgentIdentityResponse{
		AgentId: stringField(m, "agentId"),
		Name:    stringField(m, "name"),
		Emoji:   stringField(m, "emoji"),
		Avatar:  stringField(m, "avatar"),
	}, nil
}

func (s *HostService) ListModels(ctx context.Context, _ *pb.ListModelsRequest) (*pb.ListModelsResponse, error) {
	if s.reads == nil {
		return nil, status.Error(codes.Unimplemented, "reads not configured")
	}
	res, ferr := s.reads.handleModelsList(ctx, HandlerCtx{}, nil)
	if ferr != nil {
		return nil, frameErr(ferr)
	}
	raw, err := rawFromAny(res)
	if err != nil {
		return nil, internalErrf(err)
	}
	return &pb.ListModelsResponse{RawJson: raw}, nil
}

func (s *HostService) ListSessions(ctx context.Context, _ *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	if s.sessions == nil || s.chatStore == nil {
		return nil, status.Error(codes.Unimplemented, "sessions not configured")
	}
	h := NewSessionsHandler(s.sessions, s.chatStore)
	res, ferr := h.handleList(ctx, HandlerCtx{}, nil)
	if ferr != nil {
		return nil, frameErr(ferr)
	}
	raw, err := rawFromAny(res)
	if err != nil {
		return nil, internalErrf(err)
	}
	return &pb.ListSessionsResponse{RawJson: raw}, nil
}

func (s *HostService) GetChatHistory(ctx context.Context, req *pb.GetChatHistoryRequest) (*pb.GetChatHistoryResponse, error) {
	if s.chat == nil {
		return nil, status.Error(codes.Unimplemented, "chat not configured")
	}
	params, _ := json.Marshal(map[string]any{
		"sessionKey": req.GetSessionKey(),
		"limit":      req.GetLimit(),
	})
	res, ferr := s.chat.handleHistory(ctx, HandlerCtx{}, params)
	if ferr != nil {
		return nil, frameErr(ferr)
	}
	raw, err := rawFromAny(res)
	if err != nil {
		return nil, internalErrf(err)
	}
	return &pb.GetChatHistoryResponse{RawJson: raw}, nil
}

// =============================================================================
// Writes
// =============================================================================

func (s *HostService) AppendMemory(ctx context.Context, req *pb.AppendMemoryRequest) (*pb.AppendMemoryResponse, error) {
	if s.reads == nil {
		// AppendMemory needs paths to resolve the agent's workspace; the
		// reads handler is the easiest carrier for it.
		return nil, status.Error(codes.Unimplemented, "memory writer not configured")
	}
	// Mirror the same agent → workspace resolution that the WS
	// memory.append uses, so plugin writes land in the same file the
	// chat-side `remember` tool writes to.
	merged, err := config.MergedBytes(s.paths)
	if err != nil {
		return nil, internalErrf(err)
	}
	agentID := req.GetAgentId()
	if agentID == "" {
		agentID = "main"
	}
	workspace := resolveWorkspace(merged, agentID)
	if workspace == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agent %q has no resolvable workspace", agentID)
	}
	if err := memory.Append(workspace, req.GetText()); err != nil {
		return nil, internalErrf(err)
	}
	return &pb.AppendMemoryResponse{Ok: true}, nil
}

// =============================================================================
// Active actions
// =============================================================================

func (s *HostService) RunSubagent(ctx context.Context, req *pb.RunSubagentRequest) (*pb.RunSubagentResponse, error) {
	if s.chat == nil {
		return nil, status.Error(codes.Unimplemented, "chat not configured")
	}
	text, err := s.chat.RunInline(ctx, req.GetAgentId(), req.GetPrompt())
	if err != nil {
		return nil, internalErrf(err)
	}
	return &pb.RunSubagentResponse{Text: text}, nil
}

// =============================================================================
// helpers
// =============================================================================

func stringField(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
