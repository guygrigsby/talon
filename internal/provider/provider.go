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
)

// Role identifies the speaker of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
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

// Message is one turn in a conversation. Content is text-only for now;
// multi-modal content (images, audio) lands later via a content-list shape.
type Message struct {
	Role    Role
	Content string
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
//   - DeltaReasoning: Text  (carries reasoning chunk; Reasoning bool is
//                            redundant because Kind already says so)
//   - DeltaUsage:     Usage (non-nil)
//   - DeltaError:     Err   (non-nil)
type Delta struct {
	Kind  DeltaKind
	Text  string
	Usage *Usage
	Err   error
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
