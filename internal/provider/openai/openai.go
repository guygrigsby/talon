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

// Options configures a Provider. APIKey is required; the rest have defaults.
type Options struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Provider is the OpenAI streaming-completion provider.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// New constructs a Provider. APIKey is required (Stream will return a setup
// error if it is empty).
func New(opts Options) *Provider {
	p := &Provider{
		apiKey:     opts.APIKey,
		baseURL:    opts.BaseURL,
		httpClient: opts.HTTPClient,
	}
	if p.baseURL == "" {
		p.baseURL = DefaultBaseURL
	}
	if p.httpClient == nil {
		p.httpClient = http.DefaultClient
	}
	return p
}

// Name reports the provider's stable identifier ("openai").
func (p *Provider) Name() string { return "openai" }

// Stream implements provider.Provider. See package provider for channel
// semantics. The model segment of req.Model is passed to OpenAI verbatim;
// req.Model.Provider() is checked to be "openai" or "" (empty allowed for
// raw passthrough during testing).
func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Delta, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("openai: API key not configured")
	}
	if pseg := req.Model.Provider(); pseg != "" && pseg != "openai" {
		return nil, fmt.Errorf("openai: model %q is not an openai model", req.Model)
	}
	model := req.Model.Model()
	if model == "" {
		return nil, fmt.Errorf("openai: model id is empty")
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
func (p *Provider) pumpSSE(ctx context.Context, body io.ReadCloser, ch chan<- provider.Delta) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	// SSE chunks can be larger than the default 64K when prompts are long;
	// cap at 1 MiB per line.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	send := func(d provider.Delta) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- d:
			return true
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// SSE comments start with ":" — ignore.
		if strings.HasPrefix(line, ":") {
			continue
		}
		// Event-type lines ("event: ...") are not used by chat completions.
		// Only "data: ..." carries payloads.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			return
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// Treat as a soft fault — send error and stop. OpenAI emits
			// the occasional keepalive comment, but a malformed JSON
			// 'data:' is genuinely broken.
			_ = send(provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("openai: parse SSE chunk: %w", err)})
			return
		}
		// Most chunks: one choice with a delta.content fragment.
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				if !send(provider.Delta{Kind: provider.DeltaText, Text: c.Delta.Content}) {
					return
				}
			}
		}
		// The last chunk in the stream carries final usage when
		// stream_options.include_usage is true.
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
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_ = send(provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("openai: read SSE: %w", err)})
	}
}

// buildRequestBody marshals an OpenAI chat-completions request from a
// provider.Request. System prompts come in as a separate field on Request;
// here we prepend a synthetic system message because that's the OpenAI API
// shape.
func buildRequestBody(model string, req provider.Request) ([]byte, error) {
	type oaiMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type streamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	}
	type oaiRequest struct {
		Model         string         `json:"model"`
		Messages      []oaiMessage   `json:"messages"`
		Stream        bool           `json:"stream"`
		StreamOptions *streamOptions `json:"stream_options,omitempty"`
		Temperature   *float64       `json:"temperature,omitempty"`
		MaxTokens     int            `json:"max_tokens,omitempty"`
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
		msgs = append(msgs, oaiMessage{Role: role, Content: m.Content})
	}
	body := oaiRequest{
		Model:         model,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		Temperature:   req.Options.Temperature,
		MaxTokens:     req.Options.MaxOutputTokens,
	}
	return json.Marshal(body)
}

// streamChunk mirrors the relevant subset of OpenAI's chat.completion.chunk
// SSE payload. Unknown fields are ignored by encoding/json.
type streamChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// Compile-time interface assertion.
var _ provider.Provider = (*Provider)(nil)
