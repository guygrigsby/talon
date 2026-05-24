package connectapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	"github.com/guygrigsby/talon/internal/server"
)

// ChatService + SessionsService land in two stages.
//
// Stage 1 (this PR): unary methods (Send/History for chat, List/Patch
// for sessions) bridge through dispatchJSON like the rest. Streaming
// methods (chat.Subscribe, sessions.Subscribe) return Unimplemented;
// the frontend keeps using the WS path for streams until stage 2.
//
// Stage 2 (talon-y6v follow-up): refactor ChatHandler / Session to
// accept an EventSink interface so both WS and Connect can subscribe
// to the same event stream. Until then, the unary surface migrates
// without depending on the stream refactor.

// ---- ChatService ----------------------------------------------------------

type ChatService struct {
	Reg *server.Registry
}

type chatSendResp struct {
	RunID string `json:"runId"`
}

func (s *ChatService) Send(ctx context.Context, req *connect.Request[talonv1.ChatSendRequest]) (*connect.Response[talonv1.ChatSendResponse], error) {
	params := map[string]any{
		"sessionKey": req.Msg.GetSessionKey(),
		"message":    req.Msg.GetMessage(),
	}
	if k := req.Msg.GetIdempotencyKey(); k != "" {
		params["idempotencyKey"] = k
	}
	if m := req.Msg.GetModel(); m != "" {
		params["model"] = m
	}
	if sys := req.Msg.GetSystem(); sys != "" {
		params["system"] = sys
	}
	var resp chatSendResp
	if err := dispatchInto(ctx, s.Reg, "chat.send", params, &resp); err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.ChatSendResponse{RunId: resp.RunID}), nil
}

func (s *ChatService) History(ctx context.Context, req *connect.Request[talonv1.ChatHistoryRequest]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "chat.history", map[string]any{
		"sessionKey": req.Msg.GetSessionKey(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

// Subscribe is the streaming RPC the frontend will eventually use
// in place of the WS chat event stream. Stage 2 work: refactor
// ChatHandler so the event emit path can route to an EventSink
// implementation (WS or Connect's ServerStream). Until then, the
// WS path is the only way to receive chat events.
func (s *ChatService) Subscribe(_ context.Context, _ *connect.Request[talonv1.ChatSubscribeRequest], _ *connect.ServerStream[talonv1.ChatEvent]) error {
	return connect.NewError(connect.CodeUnimplemented,
		errors.New("chat.Subscribe not yet implemented over Connect (talon-y6v stage 2); use the WS /ws path for streams"))
}

// ---- SessionsService ------------------------------------------------------

type SessionsService struct {
	Reg *server.Registry
}

func (s *SessionsService) List(ctx context.Context, req *connect.Request[talonv1.Empty]) (*connect.Response[talonv1.JSONPayload], error) {
	raw, err := dispatchJSON(ctx, s.Reg, "sessions.list", nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.JSONPayload{Json: string(raw)}), nil
}

func (s *SessionsService) Patch(ctx context.Context, req *connect.Request[talonv1.SessionsPatchRequest]) (*connect.Response[talonv1.Empty], error) {
	// Patch's body is dynamic — different state slices have
	// different shapes. Hand the raw JSON to the legacy handler.
	var patch any
	if err := jsonUnmarshalAny(req.Msg.GetPatchJson(), &patch); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	params := map[string]any{
		"sessionKey": req.Msg.GetSessionKey(),
	}
	if patch != nil {
		params["patch"] = patch
	}
	_, err := dispatchJSON(ctx, s.Reg, "sessions.patch", params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.Empty{}), nil
}

// Subscribe — same Unimplemented story as ChatService.Subscribe.
// The WS sessions.subscribe path is the only way to receive
// session events until stage 2.
func (s *SessionsService) Subscribe(_ context.Context, _ *connect.Request[talonv1.SessionsSubscribeRequest], _ *connect.ServerStream[talonv1.SessionEvent]) error {
	return connect.NewError(connect.CodeUnimplemented,
		errors.New("sessions.Subscribe not yet implemented over Connect (talon-y6v stage 2); use the WS /ws path for streams"))
}
