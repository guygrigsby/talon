package agentcore_chat

import (
	"strings"

	"github.com/voocel/agentcore"
)

// EventSink is the wire-shape contract the handler implements. The
// agentcore-event → chat.event adapter calls into this; tests pass
// in a recording sink to assert behavior, and the gateway-level
// handler wraps talon's existing `ChatHandler.emit*` methods.
//
// Sink methods are called serially from one goroutine — no
// concurrent calls per session. The accumulated text passed to
// Delta is the running assistant message so far (suffix-of-prior-
// snapshots semantics, matching legacy emitChat).
type EventSink interface {
	// Delta receives a text chunk that should be appended to the
	// visible assistant reply. fullText is the running total so
	// far; deltaText is just the new chunk. Equivalent to legacy
	// emitChat(state="delta").
	Delta(fullText, deltaText string)

	// Thinking receives a hidden reasoning chunk (extended-thinking
	// models). fullText is the running reasoning trace; deltaText
	// is the new chunk. Equivalent to legacy emitChat(state="thinking").
	Thinking(fullText, deltaText string)

	// Final marks the end of the assistant turn with the final
	// visible text. Called once per agent_end. Equivalent to
	// legacy emitChat(state="final").
	Final(fullText string)

	// ToolStart fires when the model invokes a tool. argumentsJSON
	// is the literal arguments string the model emitted.
	ToolStart(toolCallID, name, argumentsJSON string)

	// ToolResult fires when the tool returns. output is the tool's
	// stringified result. isError marks tool-side failures (model-
	// visible; not transport-level).
	ToolResult(toolCallID, name, output string, isError bool)

	// Error fires for transport-level / loop-fatal errors. kind is
	// a short tag ("provider", "tool-runner-unavailable", etc.).
	Error(kind, msg string)
}

// EventAdapter translates a stream of `agentcore.Event` into calls
// on an EventSink, accumulating running text so each Delta/Thinking
// carries the full-so-far snapshot for the legacy wire contract.
//
// Lifecycle: one EventAdapter per chat-send run. Pass each event
// through Handle in order. After agent_end, the adapter is done.
type EventAdapter struct {
	sink EventSink

	// accumulated is the running visible text. Grows on every text
	// delta; reset between turns when a new message_start fires
	// for an assistant message.
	accumulated strings.Builder
	// thinking is the running reasoning trace. Same accumulation
	// rules.
	thinking strings.Builder
}

// NewEventAdapter constructs an adapter bound to a sink.
func NewEventAdapter(sink EventSink) *EventAdapter {
	return &EventAdapter{sink: sink}
}

// Handle dispatches one agentcore event onto the sink. Returns the
// number of sink calls made (useful for tests; production callers
// can ignore).
func (a *EventAdapter) Handle(ev agentcore.Event) int {
	switch ev.Type {
	case agentcore.EventMessageStart:
		// Assistant turn begins. Reset accumulators so deltas in
		// this turn start from "" instead of the previous turn's
		// trailing text.
		if ev.Message != nil && ev.Message.GetRole() == agentcore.RoleAssistant {
			a.accumulated.Reset()
			a.thinking.Reset()
		}
		return 0
	case agentcore.EventMessageUpdate:
		// Text or reasoning delta. agentcore tags it via DeltaKind.
		switch ev.DeltaKind {
		case agentcore.DeltaThinking:
			if ev.Delta == "" {
				return 0
			}
			a.thinking.WriteString(ev.Delta)
			a.sink.Thinking(a.thinking.String(), ev.Delta)
			return 1
		default:
			// Treat unknown DeltaKind as text — agentcore's default
			// is text-typed deltas, and the field is empty when the
			// delta carrier doesn't tag.
			if ev.Delta == "" {
				return 0
			}
			a.accumulated.WriteString(ev.Delta)
			a.sink.Delta(a.accumulated.String(), ev.Delta)
			return 1
		}
	case agentcore.EventMessageEnd:
		// Assistant turn fully assembled. Emit Final with the
		// running total. agentcore's Message contains the canonical
		// text, but our accumulated builder should match — prefer
		// the accumulated string so the wire matches what we
		// streamed (no chance of surprise from agentcore message
		// projection).
		if ev.Message != nil && ev.Message.GetRole() == agentcore.RoleAssistant {
			full := a.accumulated.String()
			if full == "" {
				// Some providers emit no streamed deltas — fall
				// back to the message's text content so the FE
				// still sees a final.
				full = ev.Message.TextContent()
			}
			a.sink.Final(full)
			return 1
		}
		return 0
	case agentcore.EventToolExecStart:
		a.sink.ToolStart(ev.ToolID, ev.Tool, string(ev.Args))
		return 1
	case agentcore.EventToolExecEnd:
		a.sink.ToolResult(ev.ToolID, ev.Tool, string(ev.Result), ev.IsError)
		return 1
	case agentcore.EventError:
		msg := ""
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		a.sink.Error("agent", msg)
		return 1
	}
	return 0
}

// Snapshot returns the current accumulated and thinking text. Test
// helper.
func (a *EventAdapter) Snapshot() (accumulated, thinking string) {
	return a.accumulated.String(), a.thinking.String()
}
