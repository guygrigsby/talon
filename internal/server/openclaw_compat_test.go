package server

// openclaw_compat_test.go locks in the wire-protocol invariants the
// openclaw web UI depends on. The intent is regression coverage rather
// than byte-for-byte mirroring — most assertions check that a required
// field exists with the right type or that an enum-valued field carries a
// value the UI's switch statements actually match on. Cosmetic field
// reordering or marshaller changes don't fail these tests; semantic
// drift does.
//
// Add a test here when:
//   - The UI's handleX function matches on a string value (state names,
//     role names, event types, content-block types, etc.)
//   - A new RPC handler ships and the UI consumes a structured envelope
//   - A handler's response envelope grows new required fields
//
// Keep tests focused on observable shape. Internal naming, ordering of
// optional fields, and timestamp values are deliberately not asserted.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/tidwall/gjson"
)

// jsonOf marshals v to a JSON []byte for gjson queries; t.Fatals on
// failure so the test body stays linear.
func jsonOf(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustString(t *testing.T, raw []byte, path string) string {
	t.Helper()
	r := gjson.GetBytes(raw, path)
	if !r.Exists() {
		t.Fatalf("missing %s in: %s", path, raw)
	}
	if r.Type != gjson.String {
		t.Fatalf("%s expected string, got %v: %s", path, r.Type, raw)
	}
	return r.Str
}

func mustExist(t *testing.T, raw []byte, path string) gjson.Result {
	t.Helper()
	r := gjson.GetBytes(raw, path)
	if !r.Exists() {
		t.Fatalf("missing %s in: %s", path, raw)
	}
	return r
}

// --- handshake -------------------------------------------------------------

func TestCompat_ConnectChallengeShape(t *testing.T) {
	// connect.challenge is a server-emitted event payload sent immediately
	// after the WS upgrade. The UI reads .nonce to echo (legacy clients)
	// and .ts for staleness detection.
	raw := jsonOf(t, ConnectChallenge{Nonce: "abc", Ts: 1700000000000})
	if got := mustString(t, raw, "nonce"); got == "" {
		t.Errorf("nonce must be a non-empty string, got %q", got)
	}
	if mustExist(t, raw, "ts").Type != gjson.Number {
		t.Errorf("ts must be a number")
	}
}

func TestCompat_HelloOKEnvelopeShape(t *testing.T) {
	// HelloOK is what the server replies with after a successful connect.
	// The UI's handleHello reads protocol, server.version, server.connId,
	// features.methods (to know what RPCs to call), and snapshot.* for
	// the initial dashboard population.
	hello := HelloOK{
		Type:     "hello-ok",
		Protocol: ProtocolVersion,
		Server:   ServerInfo{Version: serverVersion, ConnID: "abcdef"},
		Features: Features{Methods: []string{"chat.send", "agents.list"}, Events: []string{"chat", "agent"}},
		Snapshot: Snapshot{
			Presence:     []any{},
			Health:       map[string]any{"ok": true},
			StateVersion: StateVersion{Version: 0},
			UptimeMs:     12,
			AuthMode:     "none",
		},
		Policy: Policy{MaxPayload: 16 * 1024 * 1024, MaxBufferedBytes: 64 * 1024 * 1024, TickIntervalMs: 1000},
		Auth:   &AuthInfo{Role: "operator", Scopes: []string{"operator.read"}, IssuedAtMs: 1700000000000},
	}
	raw := jsonOf(t, hello)

	if mustString(t, raw, "type") != "hello-ok" {
		t.Errorf("type must be 'hello-ok' (UI matches on this string)")
	}
	if mustExist(t, raw, "protocol").Int() != int64(ProtocolVersion) {
		t.Errorf("protocol must echo ProtocolVersion (%d)", ProtocolVersion)
	}
	mustString(t, raw, "server.version")
	mustString(t, raw, "server.connId")
	if !mustExist(t, raw, "features.methods").IsArray() {
		t.Errorf("features.methods must be an array (UI populates capability gates from this)")
	}
	if !mustExist(t, raw, "features.events").IsArray() {
		t.Errorf("features.events must be an array")
	}
	mustExist(t, raw, "snapshot.health")
	mustExist(t, raw, "snapshot.stateVersion.version")
	mustExist(t, raw, "snapshot.uptimeMs")
	mustExist(t, raw, "policy.maxPayload")
	mustExist(t, raw, "policy.maxBufferedBytes")
	mustExist(t, raw, "policy.tickIntervalMs")
	if got := mustString(t, raw, "auth.role"); got != "operator" {
		t.Errorf("auth.role = %q, want operator", got)
	}
}

// --- agents.list -----------------------------------------------------------

func compatReadFixture(t *testing.T, openclawJSON string) openclaw.Paths {
	t.Helper()
	dir := t.TempDir()
	talonDir := filepath.Join(dir, "talon")
	openclawDir := filepath.Join(dir, "openclaw")
	if err := os.MkdirAll(talonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(openclawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	openclawCfg := filepath.Join(openclawDir, "openclaw.json")
	if openclawJSON != "" {
		if err := os.WriteFile(openclawCfg, []byte(openclawJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return openclaw.Paths{
		Talon:    openclaw.Layer{Dir: talonDir, Config: filepath.Join(talonDir, "openclaw.json")},
		Openclaw: openclaw.Layer{Dir: openclawDir, Config: openclawCfg},
	}
}

const compatRealConfig = `{
	"agents": {
		"defaults": {
			"workspace": "/ws/default",
			"model": {
				"primary": "openai/gpt-5.4-mini",
				"fallbacks": ["anthropic/claude-opus-4-7", "deepseek/deepseek-reasoner"]
			},
			"models": {
				"openai/gpt-5.4-mini": {"alias": "mini"},
				"anthropic/claude-opus-4-7": {}
			}
		},
		"list": [
			{"id": "main", "tools": {"profile": "full"}},
			{"id": "coding", "model": "anthropic/claude-opus-4-7", "name": "coding", "workspace": "/ws/coding"}
		]
	},
	"models": {
		"providers": {
			"openai":    {"models": [{"id": "gpt-5.4-mini", "name": "GPT-5.4 mini", "contextWindow": 400000, "input": ["text", "image"], "reasoning": true}]},
			"anthropic": {"models": [{"id": "claude-opus-4-7", "name": "Claude Opus 4.7", "contextWindow": 1000000, "input": ["text"], "reasoning": true}]}
		}
	}
}`

func TestCompat_AgentsListEnvelopeShape(t *testing.T) {
	h := NewReadHandler(compatReadFixture(t, compatRealConfig))
	res, ferr := h.handleAgentsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	raw := jsonOf(t, res)

	// Top-level envelope keys the UI consumes via app-gateway:
	if !mustExist(t, raw, "agents").IsArray() {
		t.Errorf("agents must be an array")
	}
	mustString(t, raw, "defaultId")
	mustString(t, raw, "mainKey")
	if got := mustString(t, raw, "scope"); got != "per-sender" {
		t.Errorf("scope = %q, want per-sender (UI gates session-routing on this string)", got)
	}
	// At least the two fixture agents present.
	if mustExist(t, raw, "agents.#").Int() < 2 {
		t.Errorf("expected ≥2 agents, got: %s", raw)
	}
}

func TestCompat_AgentsListRowShape(t *testing.T) {
	h := NewReadHandler(compatReadFixture(t, compatRealConfig))
	res, _ := h.handleAgentsList(t.Context(), HandlerCtx{}, nil)
	raw := jsonOf(t, res)

	// Find the "main" row — UI keys agent panels by id.
	mainPath := `agents.#(id="main")`
	if !gjson.GetBytes(raw, mainPath).Exists() {
		t.Fatalf("main agent missing: %s", raw)
	}
	mustString(t, raw, mainPath+".id")
	// model is required and must carry primary + fallbacks.
	mustString(t, raw, mainPath+".model.primary")
	if !mustExist(t, raw, mainPath+".model.fallbacks").IsArray() {
		t.Errorf("model.fallbacks must be an array")
	}
}

// --- models.list -----------------------------------------------------------

func TestCompat_ModelsListEnvelopeShape(t *testing.T) {
	h := NewReadHandler(compatReadFixture(t, compatRealConfig))
	res, ferr := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	raw := jsonOf(t, res)
	if !mustExist(t, raw, "models").IsArray() {
		t.Errorf("models must be an array")
	}
}

func TestCompat_ModelsListRowShape(t *testing.T) {
	h := NewReadHandler(compatReadFixture(t, compatRealConfig))
	res, _ := h.handleModelsList(t.Context(), HandlerCtx{}, nil)
	raw := jsonOf(t, res)

	row := `models.0`
	mustString(t, raw, row+".id")
	mustString(t, raw, row+".provider")
	mustString(t, raw, row+".name")
	if mustExist(t, raw, row+".contextWindow").Type != gjson.Number {
		t.Errorf("contextWindow must be a number")
	}
	if !mustExist(t, raw, row+".input").IsArray() {
		t.Errorf("input must be an array of modality strings")
	}
	// reasoning is a boolean — UI shows a thinking icon when true.
	r := gjson.GetBytes(raw, row+".reasoning")
	if !r.Exists() || (r.Type != gjson.True && r.Type != gjson.False) {
		t.Errorf("reasoning must be a boolean")
	}
}

// --- agent.identity.get ----------------------------------------------------

func TestCompat_AgentIdentityGetShape(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Clawdia\n- **Emoji:** 🦞\n- **Avatar:** avatars/x.png\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"agents":{"list":[{"id":"main","workspace":"` + wsDir + `"}]}}`
	h := NewReadHandler(compatReadFixture(t, body))
	res, ferr := h.handleAgentIdentityGet(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	raw := jsonOf(t, res)
	if mustString(t, raw, "agentId") != "main" {
		t.Errorf("agentId echo")
	}
	mustString(t, raw, "name")
	mustString(t, raw, "emoji")
	mustString(t, raw, "avatar")
}

// TestCompat_AgentIdentityGetAcceptsSessionKeyParam locks in the contract
// that broke the chat-label-shows-Assistant bug: the openclaw web UI's
// controllers/assistant-identity.ts calls
//
//	client.request("agent.identity.get", {sessionKey})
//
// and never sends agentId. A handler that requires agentId returns
// BAD_REQUEST, the UI's catch swallows it silently, and the assistant
// label sticks on "Assistant" forever.
func TestCompat_AgentIdentityGetAcceptsSessionKeyParam(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"),
		[]byte("- **Name:** Clawdia\n- **Emoji:** 🦞\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"agents":{"list":[{"id":"main","workspace":"` + wsDir + `"}]}}`
	h := NewReadHandler(compatReadFixture(t, body))

	// UI's actual call shape — sessionKey only, no agentId.
	res, ferr := h.handleAgentIdentityGet(context.Background(), HandlerCtx{}, []byte(`{"sessionKey":"agent:main:main"}`))
	if ferr != nil {
		t.Fatalf("UI's sessionKey-only request must succeed, got %+v", ferr)
	}
	if res == nil {
		t.Fatalf("expected identity payload, got nil")
	}
	raw := jsonOf(t, res)
	if mustString(t, raw, "name") != "Clawdia" {
		t.Errorf("name didn't resolve via sessionKey: %s", raw)
	}
}

// --- config.get ------------------------------------------------------------

// TestCompat_ConfigGetEnvelope locks the openclaw ConfigSnapshot shape the
// UI's controllers/config.ts consumes. Drift here breaks every config
// view (auth, channels, models, plugins, etc).
func TestCompat_ConfigGetEnvelope(t *testing.T) {
	h := NewReadHandler(compatReadFixture(t, `{"gateway":{"port":18789,"auth":{"token":"redact-me"}}}`))
	res, ferr := h.handleConfigGet(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	raw := jsonOf(t, res)
	// Required ConfigSnapshot fields per openclaw/ui/src/ui/types.ts.
	mustString(t, raw, "path")
	if mustExist(t, raw, "exists").Type != gjson.True && mustExist(t, raw, "exists").Type != gjson.False {
		t.Errorf("exists must be a bool")
	}
	mustString(t, raw, "raw")
	if h := mustString(t, raw, "hash"); len(h) != 64 {
		t.Errorf("hash should be a 64-char sha256 hex, got len=%d", len(h))
	}
	if !mustExist(t, raw, "valid").Bool() {
		t.Errorf("valid should be true for a parseable config")
	}
	if !mustExist(t, raw, "issues").IsArray() {
		t.Errorf("issues must be an array")
	}
	mustExist(t, raw, "parsed")
	mustExist(t, raw, "config")

	// Redaction: the secret must not appear anywhere in the response.
	if strings.Contains(string(raw), "redact-me") {
		t.Errorf("config.get leaked a secret")
	}
}

// --- skills.status ---------------------------------------------------------

func TestCompat_SkillsStatusEnvelopeShape(t *testing.T) {
	h := NewReadHandler(compatReadFixture(t, compatRealConfig))
	res, ferr := h.handleSkillsStatus(t.Context(), HandlerCtx{}, []byte(`{"agentId":"main"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	raw := jsonOf(t, res)
	mustString(t, raw, "workspaceDir")
	mustString(t, raw, "managedSkillsDir")
	if !mustExist(t, raw, "skills").IsArray() {
		t.Errorf("skills must be an array (empty is fine)")
	}
}

// --- chat.send response + idempotency -------------------------------------

func TestCompat_ChatSendResponseShape(t *testing.T) {
	scripted := &scriptedProvider{
		scripts: [][]provider.Delta{{{Kind: provider.DeltaText, Text: "ok"}}},
	}
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "scripted/m"}},
		&stubFactory{provider: scripted},
		NewChatStore(),
	)
	body := []byte(`{"sessionKey":"agent:main:main","message":"hi","idempotencyKey":"client-uuid-xyz"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	raw := jsonOf(t, res)
	if got := mustString(t, raw, "runId"); got != "client-uuid-xyz" {
		t.Errorf("runId must echo idempotencyKey (UI matches chat events on chatRunId === idempotencyKey), got %q", got)
	}
	waitForRunDone(t, h, "client-uuid-xyz", "agent:main:main")
}

// --- chat events -----------------------------------------------------------

func TestCompat_ChatEventDeltaShape(t *testing.T) {
	// state="delta" is the streaming-chunk signal. UI's handleChatEvent
	// switches on payload.state — a typo here breaks live token rendering.
	//
	// Protocol v4 (openclaw 150bebcd0c) makes `deltaText` required on
	// delta events; `message` remains the cumulative assistant snapshot.
	payload := chatEventPayload{
		RunID:      "run1",
		SessionKey: "agent:main:main",
		Seq:        3,
		State:      "delta",
		DeltaText:  "hi",
		Message: &chatEventMessage{
			Phase:   "assistant",
			Role:    "assistant",
			Content: []chatEventContentPart{{Type: "text", Text: "hi"}},
		},
	}
	raw := jsonOf(t, payload)
	if mustString(t, raw, "state") != "delta" {
		t.Fatalf("state must be 'delta' (UI matches this exact string)")
	}
	mustString(t, raw, "runId")
	mustString(t, raw, "sessionKey")
	mustExist(t, raw, "seq")
	if mustString(t, raw, "deltaText") != "hi" {
		t.Errorf("deltaText must carry the additive suffix (v4 contract)")
	}
	if mustString(t, raw, "message.role") != "assistant" {
		t.Errorf("message.role must be 'assistant'")
	}
	if mustString(t, raw, "message.phase") != "assistant" {
		t.Errorf("message.phase must be 'assistant' (openclaw turn-phase tag)")
	}
	if mustString(t, raw, "message.content.0.type") != "text" {
		t.Errorf("content[0].type must be 'text' for streaming chunks")
	}
}

func TestCompat_ChatEventFinalShape(t *testing.T) {
	// state="final" terminates a run. UI clears chatRunId, snapshots
	// chatStream into chatMessages. State drift here (e.g. "complete")
	// would leave the typing indicator stuck — we already shipped a fix
	// for that case (talon-11z); this test prevents recurrence.
	//
	// v4 schema for ChatFinalEventSchema is `additionalProperties:false`
	// and does not include `deltaText` — `omitempty` on the payload keeps
	// the wire form valid.
	payload := chatEventPayload{
		RunID: "r", SessionKey: "k", Seq: 7, State: "final",
		Message: &chatEventMessage{
			Phase: "assistant", Role: "assistant",
			Content: []chatEventContentPart{{Type: "text", Text: "done"}},
		},
	}
	raw := jsonOf(t, payload)
	if mustString(t, raw, "state") != "final" {
		t.Fatalf("state must be 'final' — UI's terminal-state check matches on this exact string")
	}
	if gjson.GetBytes(raw, "deltaText").Exists() {
		t.Errorf("deltaText must be absent on final events (v4 additionalProperties:false)")
	}
}

func TestCompat_ChatEventErrorShape(t *testing.T) {
	payload := chatEventPayload{
		RunID: "r", SessionKey: "k", Seq: 1, State: "error",
		ErrorKind:    "provider",
		ErrorMessage: "rate limit",
	}
	raw := jsonOf(t, payload)
	if mustString(t, raw, "state") != "error" {
		t.Fatalf("state must be 'error'")
	}
	mustString(t, raw, "errorKind")
	mustString(t, raw, "errorMessage")
}

func TestCompat_ChatEventStateValuesAreClosedSet(t *testing.T) {
	// The UI's handleChatEvent matches state against a specific closed
	// set: delta, final, aborted, error. Anything else is ignored. This
	// is a documentation test: it doesn't fail unless someone deletes
	// one of the names from emit usage.
	for _, want := range []string{"delta", "final", "aborted", "error"} {
		raw := jsonOf(t, chatEventPayload{State: want})
		if mustString(t, raw, "state") != want {
			t.Errorf("state %q didn't roundtrip", want)
		}
	}
}

// --- agent (tool) events --------------------------------------------------

func TestCompat_AgentToolStartEventShape(t *testing.T) {
	// stream="tool" + phase="start" renders the in-flight tool card.
	// data.args is decoded JSON when possible, falls back to the raw
	// argument string. data.toolCallId/name are required for the UI to
	// associate the start with its later result.
	p := buildToolStartPayload("run1", "agent:main:main", "call_x", "bash", `{"command":"ls"}`, time.Now().UnixMilli())
	raw := jsonOf(t, p)
	if mustString(t, raw, "stream") != "tool" {
		t.Fatalf("stream must be 'tool' — handleAgentEvent dispatches on this")
	}
	mustString(t, raw, "runId")
	mustString(t, raw, "sessionKey")
	mustExist(t, raw, "ts")
	if mustString(t, raw, "data.phase") != "start" {
		t.Errorf("phase must be 'start' — UI distinguishes start from result")
	}
	mustString(t, raw, "data.toolCallId")
	if mustString(t, raw, "data.name") != "bash" {
		t.Errorf("data.name carries the invoked tool name")
	}
	if mustExist(t, raw, "data.args.command").Str != "ls" {
		t.Errorf("args should be a parsed JSON object when possible")
	}
}

func TestCompat_AgentToolResultEventShape(t *testing.T) {
	p := buildToolResultPayload("run1", "agent:main:main", "call_x", "bash", "file1\nfile2\n", time.Now().UnixMilli())
	raw := jsonOf(t, p)
	if mustString(t, raw, "data.phase") != "result" {
		t.Fatalf("phase must be 'result'")
	}
	mustString(t, raw, "data.toolCallId")
	mustString(t, raw, "data.name")
	mustString(t, raw, "data.result")
}

// --- chat.history per-role row shapes -------------------------------------

func TestCompat_ChatHistoryUserRowShape(t *testing.T) {
	store := NewChatStore()
	store.Append("k", "user", "hi")

	h := NewChatHandler(&stubResolver{}, &stubFactory{}, store)
	res, _ := h.handleHistory(context.Background(), HandlerCtx{}, []byte(`{"sessionKey":"k","limit":50}`))
	raw := jsonOf(t, res)

	row := "messages.0"
	if mustString(t, raw, row+".role") != "user" {
		t.Errorf("user row role must be 'user'")
	}
	if mustString(t, raw, row+".content.0.type") != "text" {
		t.Errorf("user content block type must be 'text'")
	}
	mustExist(t, raw, row+".timestamp")
	mustString(t, raw, row+".__openclaw.id")
}

func TestCompat_ChatHistoryAssistantToolUseRowShape(t *testing.T) {
	store := NewChatStore()
	store.Append("k", "user", "x")
	store.AppendAssistantWithCalls("k", "thinking", []provider.ToolCall{
		{ID: "call_a", Name: "glob", ArgumentsJSON: `{"pattern":"*"}`},
	})
	h := NewChatHandler(&stubResolver{}, &stubFactory{}, store)
	res, _ := h.handleHistory(context.Background(), HandlerCtx{}, []byte(`{"sessionKey":"k","limit":50}`))
	raw := jsonOf(t, res)

	asst := "messages.1"
	if mustString(t, raw, asst+".role") != "assistant" {
		t.Errorf("assistant role")
	}
	// Content must include both the visible text block and a tool_use block.
	blocks := mustExist(t, raw, asst+".content").Array()
	if len(blocks) < 2 {
		t.Fatalf("expected text + tool_use blocks, got %d: %s", len(blocks), raw)
	}
	hasText, hasToolUse := false, false
	for _, b := range blocks {
		switch b.Get("type").Str {
		case "text":
			hasText = true
		case "tool_use":
			hasToolUse = true
			if b.Get("id").Str == "" || b.Get("name").Str == "" {
				t.Errorf("tool_use block missing id or name: %s", b.Raw)
			}
			if !b.Get("input").Exists() {
				t.Errorf("tool_use block must carry input")
			}
		}
	}
	if !hasText || !hasToolUse {
		t.Errorf("expected both text and tool_use blocks: %s", raw)
	}
}

func TestCompat_ChatHistoryToolResultRowShape(t *testing.T) {
	store := NewChatStore()
	store.Append("k", "user", "x")
	store.AppendAssistantWithCalls("k", "", []provider.ToolCall{
		{ID: "call_a", Name: "glob", ArgumentsJSON: `{}`},
	})
	store.AppendToolResult("k", "call_a", "glob", "main.go\n")

	h := NewChatHandler(&stubResolver{}, &stubFactory{}, store)
	res, _ := h.handleHistory(context.Background(), HandlerCtx{}, []byte(`{"sessionKey":"k","limit":50}`))
	raw := jsonOf(t, res)

	tr := "messages.2"
	// CRITICAL: role MUST be "toolResult" (the UI's matcher in
	// chat-message rendering is exactly this string). Drift to "tool"
	// breaks the tool-result card label on every reload.
	if got := mustString(t, raw, tr+".role"); got != "toolResult" {
		t.Errorf("tool result row role = %q, want 'toolResult' (UI matcher is this exact string)", got)
	}
	mustString(t, raw, tr+".toolCallId")
	if got := mustString(t, raw, tr+".toolName"); got != "glob" {
		t.Errorf("toolName must surface the invoked tool name (this is what labels the card)")
	}
	if mustExist(t, raw, tr+".isError").Type != gjson.False {
		t.Errorf("isError defaults to false")
	}
	if mustString(t, raw, tr+".content.0.type") != "text" {
		t.Errorf("tool result content block type")
	}
}

// --- protocol-level error codes -------------------------------------------

func TestCompat_FrameErrorCodesAreStable(t *testing.T) {
	// The UI maps gateway error codes to user-visible messages. A typo
	// in any of these strings would break the diagnostic UI.
	pairs := map[string]string{
		"BAD_REQUEST":         ErrCodeBadRequest,
		"UNAUTHORIZED":        ErrCodeUnauthorized,
		"PROTOCOL_MISMATCH":   ErrCodeProtocolMismatch,
		"METHOD_NOT_FOUND":    ErrCodeMethodNotFound,
		"INTERNAL":            ErrCodeInternal,
		"HANDSHAKE_REQUIRED":  ErrCodeHandshakeRequired,
	}
	for want, got := range pairs {
		if got != want {
			t.Errorf("error code drifted: var=%q, want %q", got, want)
		}
	}
}

// --- frame envelope shape (req/res/event) --------------------------------

func TestCompat_FrameTypesAreStable(t *testing.T) {
	if FrameReq != "req" {
		t.Errorf("FrameReq drift: %q", FrameReq)
	}
	if FrameRes != "res" {
		t.Errorf("FrameRes drift: %q", FrameRes)
	}
	if FrameEvent != "event" {
		t.Errorf("FrameEvent drift: %q", FrameEvent)
	}
	if ProtocolVersion != 4 {
		t.Errorf("ProtocolVersion drift: %d (UI expects 4 post-openclaw 150bebcd0c)", ProtocolVersion)
	}
}

func TestCompat_ResFrameShape(t *testing.T) {
	// res frames carry {type:"res", id, ok, payload?, error?}.
	ok := true
	f := Frame{Type: FrameRes, ID: "abc", OK: &ok, Payload: json.RawMessage(`{"x":1}`)}
	raw := jsonOf(t, f)
	if mustString(t, raw, "type") != "res" {
		t.Fatal("type")
	}
	mustString(t, raw, "id")
	if !mustExist(t, raw, "ok").Bool() {
		t.Errorf("ok must serialize as a bool when set")
	}
	mustExist(t, raw, "payload")

	// Error case: ok=false, error{code,message}
	notOK := false
	ferr := &FrameError{Code: ErrCodeBadRequest, Message: "nope"}
	f2 := Frame{Type: FrameRes, ID: "def", OK: &notOK, Error: ferr}
	raw2 := jsonOf(t, f2)
	if mustExist(t, raw2, "ok").Bool() {
		t.Errorf("ok=false must serialize as false, not omit")
	}
	if mustString(t, raw2, "error.code") != "BAD_REQUEST" {
		t.Errorf("error.code")
	}
	mustString(t, raw2, "error.message")
}

func TestCompat_EventFrameShape(t *testing.T) {
	f := Frame{Type: FrameEvent, Event: "chat", Payload: json.RawMessage(`{"runId":"x"}`)}
	raw := jsonOf(t, f)
	if mustString(t, raw, "type") != "event" {
		t.Fatal("type")
	}
	mustString(t, raw, "event")
	mustExist(t, raw, "payload")
}

// --- session-key parsing reciprocal --------------------------------------

func TestCompat_SessionKeyParsing(t *testing.T) {
	// The UI sends ?session=main as a bare string. The server must
	// resolve all three openclaw-canonical forms to the same agentId.
	cases := map[string]string{
		"main":              "main",
		"agent:main":        "main",
		"agent:main:main":   "main",
		"agent:main:custom": "main",
	}
	for in, want := range cases {
		if got := AgentIDFromSessionKey(in); got != want {
			t.Errorf("AgentIDFromSessionKey(%q) = %q, want %q", in, got, want)
		}
	}
}
