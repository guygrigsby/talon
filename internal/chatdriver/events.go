package chatdriver

import (
	"strings"

	"github.com/guygrigsby/jess/event"
)

// EventSink is the wire-shape contract the chat handler implements. The
// event adapter calls into this; tests pass a recording sink to assert
// behavior, and the gateway-level adapter wraps talon's existing
// ChatHandler.emit* methods.
//
// Calls are serialized — no concurrent calls per session. The fullText
// passed to Delta/Thinking is the running snapshot so far.
type EventSink interface {
	Delta(fullText, deltaText string)
	Thinking(fullText, deltaText string)
	Final(fullText string)
	ToolStart(toolCallID, name, argumentsJSON string)
	ToolResult(toolCallID, name, output string, isError bool)
	Error(kind, msg string)
}

// EventAdapter translates a stream of jess event.Events into EventSink
// calls, accumulating running text so each Delta/Thinking carries the
// full-so-far snapshot. Lifecycle: one EventAdapter per chat run.
// Range over run.Events() calling Handle; after the channel closes,
// call Finalize(finalText) with run.Wait()'s final assistant text.
type EventAdapter struct {
	sink        EventSink
	accumulated strings.Builder
	thinking    strings.Builder
}

func NewEventAdapter(sink EventSink) *EventAdapter {
	return &EventAdapter{sink: sink}
}

// Handle dispatches one jess event onto the sink. Returns the number of
// sink calls made (1 or 0).
func (a *EventAdapter) Handle(ev event.Event) int {
	switch ev.Kind {
	case event.KindMessageDelta:
		if ev.Delta == "" {
			return 0
		}
		if ev.DeltaKind == event.DeltaThinking {
			a.thinking.WriteString(ev.Delta)
			a.sink.Thinking(a.thinking.String(), ev.Delta)
			return 1
		}
		// DeltaText (or any other kind, including DeltaToolCall):
		// accumulate as visible text. Streamed tool-call argument
		// JSON is rendered alongside text in the same wire stream
		// today; if a host needs separate handling later, add a
		// branch.
		a.accumulated.WriteString(ev.Delta)
		a.sink.Delta(a.accumulated.String(), ev.Delta)
		return 1
	case event.KindToolStart:
		a.sink.ToolStart(ev.ToolCallID, ev.Tool, string(ev.Args))
		return 1
	case event.KindToolEnd:
		a.sink.ToolResult(ev.ToolCallID, ev.Tool, string(ev.Result), ev.IsError)
		return 1
	case event.KindError:
		msg := ""
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		a.sink.Error("agent", msg)
		return 1
	}
	return 0
}

// Finalize emits Final with the assistant text. Called by the runner
// after run.Events() closes and run.Wait() returns. If finalText is
// empty the adapter falls back to the accumulated delta total so the FE
// still sees a Final.
func (a *EventAdapter) Finalize(finalText string) {
	full := finalText
	if full == "" {
		full = a.accumulated.String()
	}
	a.sink.Final(full)
}

// Snapshot returns the current accumulators (test helper).
func (a *EventAdapter) Snapshot() (accumulated, thinking string) {
	return a.accumulated.String(), a.thinking.String()
}
