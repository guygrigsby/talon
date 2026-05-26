// Package anthropic is the Anthropic Messages API implementation of
// provider.Provider. It POSTs to /v1/messages with stream=true and
// translates the SSE event stream into provider.Delta events.
//
// API differences from OpenAI-compat that this code handles:
//
//   - System prompt is a top-level "system" param, not a message.
//   - Messages carry an array of content blocks (text, tool_use,
//     tool_result), not a flat string.
//   - Tool calls arrive as content_block events keyed by index; the
//     input JSON is streamed as input_json_delta fragments that
//     concatenate into a complete JSON object at content_block_stop.
//   - Usage is split across message_start (input tokens) and
//     message_delta (output tokens); we emit one DeltaUsage at the
//     end combining both.
//   - "thinking" content blocks (extended thinking) surface as
//     DeltaReasoning so the chat handler can render them separately
//     from the visible reply.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/guygrigsby/talon/internal/provider"
)

// DefaultBaseURL is the production Anthropic API root. Override via
// Options.BaseURL for tests against an httptest server or for
// Anthropic-compatible endpoints (Bedrock, Vertex shim, etc.).
const DefaultBaseURL = "https://api.anthropic.com/v1"

// DefaultAPIVersion is the value sent in the anthropic-version header.
// Pinned so a server-side version bump can't silently change response
// shapes — bump this constant after testing the new version.
const DefaultAPIVersion = "2023-06-01"

// DefaultMaxTokens is the floor when req.Options.MaxOutputTokens is
// zero. Anthropic's /v1/messages requires max_tokens; OpenAI does
// not. Mirroring the catalog's typical ceiling for Claude models
// keeps the request valid without forcing every caller to set it.
const DefaultMaxTokens = 8192

// Options configures a Provider. APIKey is required; the rest have
// production-Anthropic defaults.
type Options struct {
	APIKey     string
	BaseURL    string
	APIVersion string
	HTTPClient *http.Client
}

// Provider implements provider.Provider for the Anthropic Messages API.
type Provider struct {
	apiKey     string
	baseURL    string
	apiVersion string
	httpClient *http.Client
}

// New constructs a Provider. APIKey is required (Stream returns a setup
// error when empty).
func New(opts Options) *Provider {
	p := &Provider{
		apiKey:     opts.APIKey,
		baseURL:    opts.BaseURL,
		apiVersion: opts.APIVersion,
		httpClient: opts.HTTPClient,
	}
	if p.baseURL == "" {
		p.baseURL = DefaultBaseURL
	}
	if p.apiVersion == "" {
		p.apiVersion = DefaultAPIVersion
	}
	if p.httpClient == nil {
		p.httpClient = http.DefaultClient
	}
	return p
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return "anthropic" }

// Stream implements provider.Provider.
func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Delta, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("anthropic: API key not configured")
	}
	if pseg := req.Model.Provider(); pseg != "" && pseg != "anthropic" {
		return nil, fmt.Errorf("anthropic: model %q does not target provider %q", req.Model, "anthropic")
	}
	model := req.Model.Model()
	if model == "" {
		return nil, fmt.Errorf("anthropic: model id is empty")
	}

	body, err := buildRequestBody(model, req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", p.apiVersion)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: dial: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan provider.Delta)
	go p.pumpSSE(ctx, resp.Body, ch)
	return ch, nil
}

// pumpSSE reads Anthropic's SSE event stream off body, translates each
// event into Deltas, and fans them out. Always closes ch and body.
//
// Per-block state lives in `blocks` keyed by the content_block index
// the server assigns. Text blocks stream text_delta fragments straight
// to DeltaText. Tool-use blocks accumulate input_json_delta fragments
// and emit one DeltaToolCall at content_block_stop. Thinking blocks
// stream thinking_delta to DeltaReasoning.
//
// Usage is split: message_start carries input_tokens, message_delta
// carries output_tokens. We accumulate both and emit one combined
// DeltaUsage at the end of the stream.
func (p *Provider) pumpSSE(ctx context.Context, body io.ReadCloser, ch chan<- provider.Delta) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	send := func(d provider.Delta) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- d:
			return true
		}
	}

	type blockState struct {
		kind   string // "text" | "tool_use" | "thinking"
		toolID string
		toolNm string
		args   strings.Builder
	}
	blocks := map[int]*blockState{}
	var inputTokens, outputTokens int

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// SSE: lines beginning with "event:" announce the next event;
		// lines beginning with "data:" carry the JSON payload. We
		// dispatch on the data payload's "type" field, which is
		// always present and authoritative — so we can ignore the
		// event: line entirely.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			_ = send(provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("anthropic: parse SSE event: %w", err)})
			return
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil && ev.Message.Usage != nil {
				inputTokens = ev.Message.Usage.InputTokens
			}
		case "content_block_start":
			if ev.ContentBlock == nil {
				continue
			}
			b := &blockState{kind: ev.ContentBlock.Type}
			if b.kind == "tool_use" {
				b.toolID = ev.ContentBlock.ID
				b.toolNm = ev.ContentBlock.Name
			}
			blocks[ev.Index] = b
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					if !send(provider.Delta{Kind: provider.DeltaText, Text: ev.Delta.Text}) {
						return
					}
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					if !send(provider.Delta{Kind: provider.DeltaReasoning, Text: ev.Delta.Thinking}) {
						return
					}
				}
			case "input_json_delta":
				if b, ok := blocks[ev.Index]; ok && ev.Delta.PartialJSON != "" {
					b.args.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			b, ok := blocks[ev.Index]
			if !ok {
				continue
			}
			if b.kind == "tool_use" {
				if !send(provider.Delta{
					Kind: provider.DeltaToolCall,
					ToolCall: &provider.ToolCall{
						ID:            b.toolID,
						Name:          b.toolNm,
						ArgumentsJSON: b.args.String(),
					},
				}) {
					return
				}
			}
			delete(blocks, ev.Index)
		case "message_delta":
			if ev.Usage != nil {
				outputTokens += ev.Usage.OutputTokens
			}
		case "message_stop":
			// Final usage emission. Both halves are aggregated; if
			// either was zero the caller still gets a clean number.
			if inputTokens > 0 || outputTokens > 0 {
				_ = send(provider.Delta{
					Kind: provider.DeltaUsage,
					Usage: &provider.Usage{
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
					},
				})
			}
			return
		case "error":
			msg := "anthropic stream error"
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			_ = send(provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("anthropic: %s", msg)})
			return
		case "ping":
			// Keepalive — no-op.
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_ = send(provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("anthropic: read SSE: %w", err)})
	}
}

// buildRequestBody marshals an Anthropic /v1/messages request from a
// provider.Request. Messages with tool calls or tool results expand
// into content-block arrays per Anthropic's spec; plain text turns
// stay as a flat string. Adjacent same-role messages aren't merged —
// the chat handler delivers them in turn order and the API accepts
// the same shape it gets back.
func buildRequestBody(model string, req provider.Request) ([]byte, error) {
	type toolUseBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	type toolResultBlock struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		Content   string `json:"content,omitempty"`
	}
	type textBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type anthMessage struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	type anthTool struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	type anthRequest struct {
		Model       string        `json:"model"`
		System      string        `json:"system,omitempty"`
		Messages    []anthMessage `json:"messages"`
		MaxTokens   int           `json:"max_tokens"`
		Temperature *float64      `json:"temperature,omitempty"`
		Tools       []anthTool    `json:"tools,omitempty"`
		Stream      bool          `json:"stream"`
	}

	msgs := make([]anthMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := string(m.Role)
		switch role {
		case "system":
			// System messages aren't allowed in messages[]; if one
			// snuck in (older harness shapes), merge it into the
			// top-level System parameter instead of dropping it.
			if req.System == "" {
				req.System = m.Content
			} else {
				req.System += "\n\n" + m.Content
			}
			continue
		case "tool":
			// Tool results: a USER turn carrying a tool_result
			// content block. Anthropic doesn't use a "tool" role.
			msgs = append(msgs, anthMessage{
				Role: "user",
				Content: []toolResultBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				}},
			})
			continue
		case "":
			role = "user"
		}

		if len(m.ToolCalls) == 0 {
			msgs = append(msgs, anthMessage{Role: role, Content: m.Content})
			continue
		}
		// Assistant turn invoking tools. Anthropic expects a
		// content array combining optional text plus one tool_use
		// block per call.
		blocks := []any{}
		if m.Content != "" {
			blocks = append(blocks, textBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			args := json.RawMessage(tc.ArgumentsJSON)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			blocks = append(blocks, toolUseBlock{
				Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: args,
			})
		}
		msgs = append(msgs, anthMessage{Role: role, Content: blocks})
	}

	body := anthRequest{
		Model:       model,
		System:      req.System,
		Messages:    msgs,
		MaxTokens:   req.Options.MaxOutputTokens,
		Temperature: req.Options.Temperature,
		Stream:      true,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = DefaultMaxTokens
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]anthTool, len(req.Tools))
		for i, t := range req.Tools {
			body.Tools[i] = anthTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.ParametersSchema,
			}
		}
	}
	return json.Marshal(body)
}

// sseEvent mirrors the subset of Anthropic's SSE payload we use.
// Unknown fields are ignored by encoding/json. Pointers gate which
// sub-objects are present per event type (see pumpSSE).
type sseEvent struct {
	Type         string           `json:"type"`
	Index        int              `json:"index,omitempty"`
	Message      *sseMessageStart `json:"message,omitempty"`
	ContentBlock *sseContentBlock `json:"content_block,omitempty"`
	Delta        *sseDelta        `json:"delta,omitempty"`
	Usage        *sseUsage        `json:"usage,omitempty"`
	Error        *sseError        `json:"error,omitempty"`
}

type sseMessageStart struct {
	Usage *sseUsage `json:"usage,omitempty"`
}

type sseContentBlock struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type sseDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	// StopReason rides on message_delta but we don't currently key
	// off it — the message_stop event already signals end-of-stream.
	StopReason string `json:"stop_reason,omitempty"`
}

type sseUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

type sseError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Compile-time interface assertion.
var _ provider.Provider = (*Provider)(nil)
