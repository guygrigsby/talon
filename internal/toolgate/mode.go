package toolgate

import "strings"

// Mode controls how the gate acts on its verdicts (ADR 0017, Phase 6).
//
//   - ModeEnforce (default): non-Allow verdicts block, returning a refusal.
//   - ModeAudit: every call is classified and recorded but always executes.
//   - ModeOff: gating is skipped entirely (callers should not wrap at all).
//
// Enforce is the zero value and the fail-safe default: an unknown or empty
// configured mode resolves to enforce.
type Mode int

const (
	ModeEnforce Mode = iota
	ModeAudit
	ModeOff
)

// ParseMode resolves a configured mode string. Unknown/empty values default to
// ModeEnforce (fail-safe). Case-insensitive.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "audit":
		return ModeAudit
	case "off":
		return ModeOff
	default:
		return ModeEnforce
	}
}

func (m Mode) String() string {
	switch m {
	case ModeAudit:
		return "audit"
	case ModeOff:
		return "off"
	default:
		return "enforce"
	}
}
