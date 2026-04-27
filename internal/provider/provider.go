// Package provider defines the streaming-completion provider interface that
// talon-gateway's chat.send handler uses to drive an LLM. Concrete provider
// implementations live in subpackages (provider/openai, provider/deepseek,
// ...). Each concrete provider owns its own credential plumbing and SDK/HTTP
// dependency; the interface here is deliberately credential-agnostic so the
// gateway code that wires sessions to providers does not depend on any one
// provider's auth shape.
package provider

import (
	"context"
	"encoding/json"
)

// Role identifies the speaker of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	// RoleTool is a tool-result message in a multi-turn loop. It carries
	// ToolCallID identifying the originating tool_use and Content with
	// the JSON-serialized result.
	RoleTool Role = "tool"
)

// ModelID is the canonical "<provider>/<model>" form used throughout
// openclaw and talon (e.g. "openai/gpt-5.4-mini"). Providers split on the
// first "/" — anything to the left is the provider key, anything to the
// right is the provider-specific model identifier.
type ModelID string

// Provider returns the provider segment of m, or "" if m is malformed.
func (m ModelID) Provider() string {
	for i := 0; i < len(m); i++ {
		if m[i] == '/' {
			return string(m[:i])
		}
	}
	return ""
}

// Model returns the model segment of m (the part after the first "/").
// Returns the whole string when there is no "/".
func (m ModelID) Model() string {
	for i := 0; i < len(m); i++ {
		if m[i] == '/' {
			return string(m[i+1:])
		}
	}
	return string(m)
}

// Message is one turn in a conversation. Content is text — plain for
// user/system/assistant turns. Tool-related turns:
//
//   - Role=Assistant + ToolCalls non-empty: the model chose to invoke
//     tools this turn. Content may also be non-empty if the model emitted
//     visible text alongside the calls.
//   - Role=Tool: a tool result. Content carries the tool's output
//     (typically JSON or text); ToolCallID identifies which tool_use this
//     is the result of.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant turns invoking tools
	ToolCallID string     // role=tool only
}

// Options carries non-required, per-request tuning. Provider impls should
// translate the platform-agnostic fields and pass anything in Extra through
// to the provider's native API where supported.
type Options struct {
	// Temperature, if non-nil, overrides the provider default. Range is
	// provider-defined (commonly 0..2).
	Temperature *float64
	// MaxOutputTokens caps the streamed response length. 0 means unset
	// (let the provider's default apply).
	MaxOutputTokens int
	// Extra carries provider-specific options the abstraction does not
	// model (e.g. OpenAI "logit_bias", DeepSeek "frequency_penalty").
	// Concrete providers ignore unknown keys.
	Extra map[string]any
}

// Request is the streaming-completion request handed to a Provider.
type Request struct {
	Model    ModelID
	System   string    // optional system prompt; empty means none
	Messages []Message // chat history, oldest first
	Options  Options
	// Tools advertises the function/tool surface the model may invoke
	// during this completion. Empty disables tool-calling for this turn.
	Tools []ToolSpec
}

// ToolSpec describes one callable tool exposed to the model. ParametersSchema
// is the JSON schema for the tool's input arguments — providers map this
// onto their own request shape (OpenAI: tools[].function.parameters;
// Anthropic: tools[].input_schema; etc).
type ToolSpec struct {
	Name             string
	Description      string
	ParametersSchema json.RawMessage
}

// ToolCall is the assembled tool-call payload from a streaming response. The
// provider buffers per-call argument fragments internally and emits one
// ToolCall (via DeltaToolCall) when the call is finalized — typically when
// the model emits finish_reason="tool_calls" or the stream ends.
type ToolCall struct {
	// ID is the call site identifier the provider returned. Echo this
	// back as tool_use_id when sending the result in a follow-up turn.
	ID string
	// Name matches the ToolSpec.Name the model chose to invoke.
	Name string
	// ArgumentsJSON is the model's chosen input as a JSON-encoded string.
	// Providers stream it in fragments; the consumer can decode this once
	// it's complete.
	ArgumentsJSON string
}

// DeltaKind discriminates a Delta variant. Each variant uses a distinct
// subset of Delta's fields — see the Delta godoc for which apply.
type DeltaKind int

const (
	// DeltaText is a textual token chunk in the assistant response.
	// Concatenating Text across all DeltaText events produces the visible
	// reply.
	DeltaText DeltaKind = iota
	// DeltaReasoning is a chunk of the model's hidden reasoning trace
	// (o1/Claude-thinking style). Most providers will not emit this;
	// the assembled string is informative, not part of the user-visible
	// reply.
	DeltaReasoning
	// DeltaUsage is sent at most once per stream, near the end, with
	// final input/output token counts. Usage is non-nil for this kind.
	DeltaUsage
	// DeltaToolCall is emitted once per assembled tool call when the
	// stream finalizes the call (typically at finish_reason="tool_calls"
	// or end-of-stream). ToolCall is non-nil. A single stream may emit
	// multiple DeltaToolCall events when the model chose more than one
	// tool to invoke this turn.
	DeltaToolCall
	// DeltaError indicates a mid-stream failure. Err is non-nil and the
	// channel is closed immediately after this delta. Setup-time
	// failures should be returned from Provider.Stream, not surfaced
	// here.
	DeltaError
)

// Usage is the accounting payload of a DeltaUsage event.
type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // 0 if the provider does not report it separately
}

// Delta is a single event in a streaming response. Which fields are set
// depends on Kind:
//   - DeltaText:      Text
//   - DeltaReasoning: Text     (reasoning chunk; not part of visible reply)
//   - DeltaUsage:     Usage    (non-nil)
//   - DeltaToolCall:  ToolCall (non-nil; assembled, not fragments)
//   - DeltaError:     Err      (non-nil)
type Delta struct {
	Kind     DeltaKind
	Text     string
	Usage    *Usage
	ToolCall *ToolCall
	Err      error
}

// Provider is a streaming-completion source. Implementations are expected
// to be safe for concurrent use across distinct Stream calls; a single
// returned channel must not be read by more than one consumer.
type Provider interface {
	// Name returns the provider's stable identifier — must match the
	// provider segment of ModelID for any model the provider serves
	// (e.g. "openai", "deepseek", "anthropic").
	Name() string

	// Stream initiates a streaming completion. The returned channel
	// receives Deltas in arrival order and is closed when the stream
	// terminates: a normal end-of-stream (after the optional DeltaUsage),
	// a DeltaError event (always followed by close), or context
	// cancellation (close without a final delta — caller should consult
	// ctx.Err()).
	//
	// Setup errors (auth missing, model unknown, request malformed) are
	// returned synchronously via the error return; in those cases the
	// channel will be nil. Mid-stream errors arrive on the channel as a
	// DeltaError, after which the channel is closed.
	Stream(ctx context.Context, req Request) (<-chan Delta, error)
}
