package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type bashTool struct{ ws string }

func (t *bashTool) Name() string { return "bash" }

func (t *bashTool) Description() string {
	return "Run a shell command via /bin/sh -c in the workspace. Captures stdout+stderr; default timeout 30s."
}

func (t *bashTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string"},
			"timeout_seconds": {"type": "integer", "description": "Max wall-clock seconds. Default 30, max 300.", "minimum": 1}
		},
		"required": ["command"]
	}`)
}

func (t *bashTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var p struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("bash: %w", err)
	}
	if strings.TrimSpace(p.Command) == "" {
		return "", fmt.Errorf("bash: command is required")
	}
	// Defense-in-depth denylist. Returns a non-error response so
	// the model sees the rejection reason and can adapt — vs. an
	// error that would surface as a generic tool failure.
	if reason := bashDenylistViolation(p.Command); reason != "" {
		return "bash: " + reason, nil
	}
	timeout := 30 * time.Second
	if p.TimeoutSeconds > 0 {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", p.Command)
	cmd.Dir = t.ws
	out, err := cmd.CombinedOutput()
	// Always include stdout+stderr in the returned string — the model
	// often needs the output even on non-zero exit.
	body := string(out)
	if err != nil {
		// ExitError: include exit code in the trailer so the model can
		// reason about success/failure.
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			return body + fmt.Sprintf("\n[exit %d]", exitErr.ExitCode()), nil
		}
		if runCtx.Err() == context.DeadlineExceeded {
			return body + fmt.Sprintf("\n[timed out after %s]", timeout), nil
		}
		return body, fmt.Errorf("bash: %w", err)
	}
	return body, nil
}

// asExit is a tiny errors.As helper kept inline so the file doesn't need
// to import "errors" just for one call.
func asExit(err error, target **exec.ExitError) bool {
	for e := err; e != nil; {
		if v, ok := e.(*exec.ExitError); ok {
			*target = v
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
