package connectapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	"github.com/guygrigsby/talon/internal/server"
)

// ChatService + SessionsService land in two stages.
//
// Stage 1 (current): unary methods (chat Send/History, sessions
// List/Patch) bridge through dispatchJSON. The Subscribe stream
// for chat returns CodeUnimplemented; the frontend keeps using the
// WS path for live events. The proto contract for the typed
// ChatEvent oneof is locked here even though the wiring is still
// stage 2, so the FE can codegen its discriminated-union types
// today.
//
// Stage 2 (talon-y6v follow-up): refactor ChatHandler / Session
// to accept an EventSink interface so both WS and Connect can
// subscribe to the same event stream without duplicating emit
// sites. No proto changes needed at that point.

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
	var resp chatSendResp
	if err := dispatchInto(ctx, s.Reg, "chat.send", params, &resp); err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.ChatSendResponse{RunId: resp.RunID}), nil
}

// chatHistoryRaw decodes the openclaw row shape produced by the
// chat.history WS handler. Kept package-private; only History uses
// it. Fields that are absent in a given row variant just stay zero
// (no per-variant struct needed because the variant is selected by
// role, not by a tag field).
type chatHistoryRaw struct {
	Messages []chatHistoryRow `json:"messages"`
}

type chatHistoryRow struct {
	Meta       chatHistoryMeta        `json:"__openclaw"`
	Role       string                 `json:"role"`
	Content    []chatHistoryContent   `json:"content"`
	ToolCallID string                 `json:"toolCallId,omitempty"`
	ToolName   string                 `json:"toolName,omitempty"`
}

type chatHistoryMeta struct {
	ID  string `json:"id"`
	Seq int    `json:"seq"`
}

type chatHistoryContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

func (s *ChatService) History(ctx context.Context, req *connect.Request[talonv1.ChatHistoryRequest]) (*connect.Response[talonv1.ChatHistoryResponse], error) {
	params := map[string]any{
		"sessionKey": req.Msg.GetSessionKey(),
	}
	if lim := req.Msg.GetLimit(); lim > 0 {
		params["limit"] = lim
	}
	raw, err := dispatchJSON(ctx, s.Reg, "chat.history", params)
	if err != nil {
		return nil, err
	}
	var decoded chatHistoryRaw
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("chat.history: decode response: %w", err))
	}
	out := &talonv1.ChatHistoryResponse{
		Messages: make([]*talonv1.HistoryRow, 0, len(decoded.Messages)),
	}
	for _, row := range decoded.Messages {
		if proto := historyRowToProto(row); proto != nil {
			out.Messages = append(out.Messages, proto)
		}
	}
	return connect.NewResponse(out), nil
}

// historyRowToProto converts one openclaw-shaped history row into
// the typed HistoryRow variant. Returns nil for roles the proto
// doesn't model (e.g. "system" — never persisted today but cheap
// to handle defensively) so unknown rows are dropped rather than
// surfaced as empty bodies.
func historyRowToProto(row chatHistoryRow) *talonv1.HistoryRow {
	hr := &talonv1.HistoryRow{
		Id:  row.Meta.ID,
		Seq: int32(row.Meta.Seq),
	}
	switch row.Role {
	case "user":
		hr.Body = &talonv1.HistoryRow_User{
			User: &talonv1.UserMessage{Text: firstText(row.Content)},
		}
	case "assistant":
		am := &talonv1.AssistantMessage{}
		for _, c := range row.Content {
			switch c.Type {
			case "text":
				am.Text = c.Text
			case "tool_use":
				am.ToolUses = append(am.ToolUses, &talonv1.ToolUseMessage{
					ToolCallId: c.ID,
					Name:       c.Name,
					ArgsJson:   string(c.Input),
				})
			}
		}
		// Pure tool-use turn: surface as ToolUseMessage variant so
		// the FE can render it as a tool card without a phantom
		// assistant bubble. Multi-tool turns still use Assistant
		// (they belong to one assistant message).
		if am.Text == "" && len(am.ToolUses) == 1 {
			hr.Body = &talonv1.HistoryRow_ToolUse{ToolUse: am.ToolUses[0]}
		} else {
			hr.Body = &talonv1.HistoryRow_Assistant{Assistant: am}
		}
	case "toolResult":
		hr.Body = &talonv1.HistoryRow_ToolResult{
			ToolResult: &talonv1.ToolResultMessage{
				ToolCallId: row.ToolCallID,
				Name:       row.ToolName,
				Output:     firstText(row.Content),
			},
		}
	default:
		return nil
	}
	return hr
}

// firstText returns the first content block's text, or "". Used by
// rows whose content shape is a flat single-text-block (user,
// toolResult).
func firstText(parts []chatHistoryContent) string {
	for _, p := range parts {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}

// Subscribe is the typed streaming RPC the frontend will use in
// place of the WS chat event stream. Stage 2 work: refactor
// ChatHandler so the event-emit path can route to an EventSink
// implementation (WS frame or Connect's ServerStream). Until then,
// the WS path is the only way to receive chat events. The proto
// contract (ChatEvent oneof variants) is locked here so the FE
// can codegen against it now and stage 2 lands transparently.
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
