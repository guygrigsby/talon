// Package modeltest provides a scripted, deterministic jess model.Model for
// tests. It replays a fixed program of turns — reasoning, text deltas, and
// tool calls — so the full chatdriver -> jess -> agentcore loop can be
// exercised without a network LLM. Each call to Stream consumes the next
// scripted Turn, which models the loop calling back after running a tool.
//
// This is the local mirror of an intended upstream jess/modeltest package
// (ADR 0016); swap to the upstream import on the next jess bump.
package modeltest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

// ToolCall is one tool invocation the scripted model emits in a turn.
type ToolCall struct {
	ID   string
	Name string
	Args string // raw JSON arguments
}

// Turn is one scripted model response (what a single Stream call emits).
type Turn struct {
	Reasoning  []string   // each emitted as a DeltaThinking chunk, in order
	Text       []string   // each emitted as a DeltaText chunk, in order
	ToolCalls  []ToolCall // tool-call blocks placed on the final Done message
	Usage      model.Usage
	StopReason string
}

// Call records the inputs to one Stream invocation, for assertions.
type Call struct {
	Messages []message.Message
	Tools    []model.ToolSpec
}

// Model is a deterministic, scripted model.Model.
type Model struct {
	turns   []Turn
	noTools bool
	mu      sync.Mutex
	callIdx int
	calls   []Call
}

// New builds a scripted model that replays the given turns, one per Stream
// call. With no turns it still answers (each call overruns -> Err chunk),
// which is handy for the SupportsTools-only checks.
func New(turns ...Turn) *Model {
	return &Model{turns: turns}
}

// WithToolsUnsupported makes SupportsTools report false.
func (m *Model) WithToolsUnsupported() *Model {
	m.noTools = true
	return m
}

// SupportsTools reports whether the model accepts tool specs.
func (m *Model) SupportsTools() bool { return !m.noTools }

// Calls returns the captured inputs of every Stream invocation so far.
func (m *Model) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Call, len(m.calls))
	copy(out, m.calls)
	return out
}

// Stream replays the next scripted turn. The returned channel emits delta
// chunks (reasoning then text) followed by exactly one Done chunk carrying the
// assembled assistant message — or a single Err chunk if the script is
// overrun or context is cancelled.
func (m *Model) Stream(ctx context.Context, msgs []message.Message, tools []model.ToolSpec) (<-chan model.Chunk, error) {
	m.mu.Lock()
	idx := m.callIdx
	m.callIdx++
	m.calls = append(m.calls, Call{Messages: append([]message.Message(nil), msgs...), Tools: append([]model.ToolSpec(nil), tools...)})
	m.mu.Unlock()

	ch := make(chan model.Chunk)
	go func() {
		defer close(ch)
		send := func(c model.Chunk) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- c:
				return true
			}
		}

		if idx >= len(m.turns) {
			send(model.Chunk{Err: fmt.Errorf("modeltest: Stream called %d time(s) but only %d turn(s) scripted", idx+1, len(m.turns))})
			return
		}
		turn := m.turns[idx]

		for _, r := range turn.Reasoning {
			if !send(model.Chunk{Delta: r, DeltaKind: event.DeltaThinking}) {
				return
			}
		}
		for _, t := range turn.Text {
			if !send(model.Chunk{Delta: t, DeltaKind: event.DeltaText}) {
				return
			}
		}
		send(model.Chunk{
			Done:       true,
			Message:    assembleMessage(turn),
			Usage:      turn.Usage,
			StopReason: turn.StopReason,
		})
	}()
	return ch, nil
}

// assembleMessage builds the complete assistant message for a turn: the joined
// text as a text block, followed by one tool_call block per scripted call.
func assembleMessage(turn Turn) message.Message {
	var content []message.ContentBlock
	var text string
	for _, t := range turn.Text {
		text += t
	}
	if text != "" {
		content = append(content, message.ContentBlock{Kind: message.BlockText, Text: text})
	}
	for _, tc := range turn.ToolCalls {
		content = append(content, message.ContentBlock{
			Kind:     message.BlockToolCall,
			ToolID:   tc.ID,
			ToolName: tc.Name,
			Args:     json.RawMessage(tc.Args),
		})
	}
	return message.Message{Role: message.RoleAssistant, Content: content}
}
