// Package audit records agent actions to a durable, redacted, correlated
// trail so a session can be traced after a failure. The Event type is
// talon-owned and source-agnostic: today it's populated from the agentcore
// event stream, but nothing here depends on agentcore (see ADR 0011 / talon-17z).
package audit

import "time"

type EventKind string

const (
	KindTurnStart  EventKind = "turn_start"
	KindToolCall   EventKind = "tool_call"
	KindToolResult EventKind = "tool_result"
	KindMessage    EventKind = "message"
	KindError      EventKind = "error"
	KindTurnEnd    EventKind = "turn_end"
)

// Event is one recorded agent action. Correlation: Session+Run+Seq order a
// run's actions. Secret-bearing fields (Args, Output, Text) are redacted and
// bounded by the recorder before they hit disk.
type Event struct {
	Ts         time.Time `json:"ts"`
	Kind       EventKind `json:"kind"`
	Session    string    `json:"session"`
	Run        string    `json:"run"`
	Agent      string    `json:"agent,omitempty"`
	Seq        int64     `json:"seq"`
	Model      string    `json:"model,omitempty"`      // turn_start
	Tool       string    `json:"tool,omitempty"`       // tool_call / tool_result
	ToolCallID string    `json:"toolCallId,omitempty"` // tool_call / tool_result
	Args       string    `json:"args,omitempty"`       // tool_call (redacted)
	Output     string    `json:"output,omitempty"`     // tool_result (redacted)
	IsError    bool      `json:"isError,omitempty"`    // tool_result
	Text       string    `json:"text,omitempty"`       // message summary
	ErrKind    string    `json:"errKind,omitempty"`    // error
	ErrMsg     string    `json:"errMsg,omitempty"`     // error
}
