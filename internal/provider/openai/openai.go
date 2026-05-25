// Package openai is the OpenAI concrete implementation of provider.Provider.
// It calls the chat-completions endpoint with stream=true and translates
// the SSE chunk stream into provider.Delta events.
package openai

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

// DefaultBaseURL is the production OpenAI API root. Override via Options.BaseURL
// for tests against an httptest server, or to point at an OpenAI-compatible
// endpoint (Azure, Together, etc).
const DefaultBaseURL = "https://api.openai.com/v1"

// Options configures a Provider. APIKey is required; the rest have defaults
// suitable for OpenAI's own API. To target an OpenAI-compatible provider
// (DeepSeek, Together, Azure-OpenAI, etc.) override BaseURL plus Name +
// ProviderKey so the Provider identifies itself correctly and validates
// incoming ModelIDs against the right provider segment.
type Options struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	// Name overrides Provider.Name(). Default "openai".
	Name string
	// ProviderKey is the expected ModelID provider segment. Stream
	// rejects ModelIDs whose Provider() segment differs from this (an
	// empty segment is always accepted for raw passthrough during
	// testing). Default "openai".
	ProviderKey string
}

// Provider is an OpenAI-compatible streaming-completion provider.
type Provider struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	name        string
	providerKey string
}

// New constructs a Provider. APIKey is required (Stream will return a setup
// error if it is empty).
func New(opts Options) *Provider {
	p := &Provider{
		apiKey:      opts.APIKey,
		baseURL:     opts.BaseURL,
		httpClient:  opts.HTTPClient,
		name:        opts.Name,
		providerKey: opts.ProviderKey,
	}
	if p.baseURL == "" {
		p.baseURL = DefaultBaseURL
	}
	if p.httpClient == nil {
		p.httpClient = http.DefaultClient
	}
	if p.name == "" {
		p.name = "openai"
	}
	if p.providerKey == "" {
		p.providerKey = "openai"
	}
	return p
}

// Name reports the provider's stable identifier (default "openai"; override
// via Options.Name for compatible providers).
func (p *Provider) Name() string { return p.name }

// Stream implements provider.Provider. See package provider for channel
// semantics. The model segment of req.Model is passed to OpenAI verbatim;
// req.Model.Provider() is checked to be "openai" or "" (empty allowed for
// raw passthrough during testing).
func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Delta, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("%s: API key not configured", p.name)
	}
	if pseg := req.Model.Provider(); pseg != "" && pseg != p.providerKey {
		return nil, fmt.Errorf("%s: model %q does not target provider %q", p.name, req.Model, p.providerKey)
	}
	model := req.Model.Model()
	if model == "" {
		return nil, fmt.Errorf("%s: model id is empty", p.name)
	}

	body, err := buildRequestBody(model, req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: dial: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Drain a small error body so the user sees the API's reason.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		resp.Body.Close()
		return nil, fmt.Errorf("openai: http %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan provider.Delta)
	go p.pumpSSE(ctx, resp.Body, ch)
	return ch, nil
}

// pumpSSE reads OpenAI SSE chunks off body, translates each to a Delta, and
// fans them out to ch until [DONE] or EOF. Always closes ch and body.
//
// Tool calls arrive as multi-chunk fragments keyed by an index inside
// delta.tool_calls[]. We buffer them in toolCallsByIndex and emit one
// provider.ToolCall (DeltaToolCall) per index when the stream finalizes —
// either when finish_reason becomes "tool_calls" or at end-of-stream.
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

	type accCall struct {
		id   string
		name string
		args strings.Builder
	}
	tools := map[int]*accCall{}
	flushTools := func() bool {
		if len(tools) == 0 {
			return true
		}
		// Emit in index order so multi-tool turns are reproducible.
		idxs := make([]int, 0, len(tools))
		for i := range tools {
			idxs = append(idxs, i)
		}
		sortInts(idxs)
		for _, i := range idxs {
			c := tools[i]
			if c.id == "" && c.name == "" && c.args.Len() == 0 {
				continue
			}
			if !send(provider.Delta{
				Kind: provider.DeltaToolCall,
				ToolCall: &provider.ToolCall{
					ID:            c.id,
					Name:          c.name,
					ArgumentsJSON: c.args.String(),
				},
			}) {
				return false
			}
		}
		tools = map[int]*accCall{}
		return true
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			flushTools()
			return
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			_ = send(provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("openai: parse SSE chunk: %w", err)})
			return
		}
		for _, c := range chunk.Choices {
			// Text fragment.
			if c.Delta.ReasoningContent != "" {
				if !send(provider.Delta{Kind: provider.DeltaReasoning, Text: c.Delta.ReasoningContent}) {
					return
				}
			}
			if c.Delta.Content != "" {
				if !send(provider.Delta{Kind: provider.DeltaText, Text: c.Delta.Content}) {
					return
				}
			}
			// Tool call fragments — accumulate per-index.
			for _, tcf := range c.Delta.ToolCalls {
				acc, ok := tools[tcf.Index]
				if !ok {
					acc = &accCall{}
					tools[tcf.Index] = acc
				}
				if tcf.ID != "" {
					acc.id = tcf.ID
				}
				if tcf.Function.Name != "" {
					acc.name = tcf.Function.Name
				}
				if tcf.Function.Arguments != "" {
					acc.args.WriteString(tcf.Function.Arguments)
				}
			}
			// finish_reason="tool_calls" closes the model's turn — flush
			// accumulated calls now so downstream can run them in
			// parallel with usage processing.
			if c.FinishReason == "tool_calls" {
				if !flushTools() {
					return
				}
			}
		}
		if chunk.Usage != nil {
			if !send(provider.Delta{
				Kind: provider.DeltaUsage,
				Usage: &provider.Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
				},
			}) {
				return
			}
		}
	}
	// Stream ended without a [DONE] sentinel — flush any pending tools.
	flushTools()
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_ = send(provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("openai: read SSE: %w", err)})
	}
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// buildRequestBody marshals an OpenAI chat-completions request from a
// provider.Request. System prompts come in as a separate field on Request;
// here we prepend a synthetic system message because that's the OpenAI API
// shape. Tool turns translate as follows:
//
//   - Role=Assistant with ToolCalls → emits {role:"assistant",
//     tool_calls:[{id,function:{name,arguments}}]}.
//   - Role=Tool → emits {role:"tool", tool_call_id, content}.
func buildRequestBody(model string, req provider.Request) ([]byte, error) {
	type oaiToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	// Content is always emitted (no omitempty). OpenAI maps a missing
	// content field to null and rejects messages where the schema
	// expects a string ("Invalid value for 'content': expected a string,
	// got null"). Empty string is accepted everywhere — including
	// assistant turns that only carry tool_calls and tool result turns
	// where the tool produced no output.
	type oaiMessage struct {
		Role       string        `json:"role"`
		Content    string        `json:"content"`
		ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
		ToolCallID string        `json:"tool_call_id,omitempty"`
	}
	type streamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	}
	type oaiToolFunction struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	type oaiTool struct {
		Type     string          `json:"type"`
		Function oaiToolFunction `json:"function"`
	}
	type oaiRequest struct {
		Model         string         `json:"model"`
		Messages      []oaiMessage   `json:"messages"`
		Stream        bool           `json:"stream"`
		StreamOptions *streamOptions `json:"stream_options,omitempty"`
		Temperature   *float64       `json:"temperature,omitempty"`
		MaxTokens     int            `json:"max_tokens,omitempty"`
		Tools         []oaiTool      `json:"tools,omitempty"`
	}
	msgs := make([]oaiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, oaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		role := string(m.Role)
		if role == "" {
			role = "user"
		}
		om := oaiMessage{Role: role, Content: m.Content, ToolCallID: m.ToolCallID}
		if len(m.ToolCalls) > 0 {
			om.ToolCalls = make([]oaiToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				om.ToolCalls[i].ID = tc.ID
				om.ToolCalls[i].Type = "function"
				om.ToolCalls[i].Function.Name = tc.Name
				om.ToolCalls[i].Function.Arguments = tc.ArgumentsJSON
			}
		}
		msgs = append(msgs, om)
	}
	body := oaiRequest{
		Model:         model,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		Temperature:   req.Options.Temperature,
		MaxTokens:     req.Options.MaxOutputTokens,
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]oaiTool, len(req.Tools))
		for i, t := range req.Tools {
			body.Tools[i] = oaiTool{
				Type: "function",
				Function: oaiToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.ParametersSchema,
				},
			}
		}
	}
	return json.Marshal(body)
}

// streamChunk mirrors the relevant subset of OpenAI's chat.completion.chunk
// SSE payload. Unknown fields are ignored by encoding/json.
type streamChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string             `json:"role,omitempty"`
			Content   string             `json:"content,omitempty"`
			ToolCalls []streamToolCallFr `json:"tool_calls,omitempty"`
			// ReasoningContent is DeepSeek Reasoner's hidden
			// reasoning trace; OpenAI's o-series uses the same
			// field name on the responses-streaming variant.
			// Surfaces as provider.DeltaReasoning so the chat
			// handler can emit a "thinking" event distinct from
			// the visible reply.
			ReasoningContent string `json:"reasoning_content,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// streamToolCallFr is the per-chunk tool-call fragment emitted by OpenAI.
// On the first chunk for a given index, ID + Function.Name are present;
// subsequent chunks carry only Function.Arguments fragments. We accumulate
// per-index (since OpenAI may stream multiple parallel tool calls) and emit
// one assembled provider.ToolCall when the stream ends.
type streamToolCallFr struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

// Compile-time interface assertion.
var _ provider.Provider = (*Provider)(nil)
