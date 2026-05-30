package chatdriver

import (
	"context"
	"sync"
	"testing"

	"github.com/guygrigsby/talon/internal/talonpath"
)

// The runner's early-return paths (config load, BuildAgent, NewSessionWithHistory,
// Prompt) must emit a sink error before returning — the server goroutine doesn't
// emit one for a returned err, and without the emit the FE sees a silent failure.
// This test forces the runner to fail early (empty Paths -> some early stage
// fails) and asserts emitError was called.
func TestRunner_EmitsSinkErrorOnEarlyFailure(t *testing.T) {
	var (
		mu       sync.Mutex
		errCalls int
		gotKind  string
		gotMsg   string
	)

	runner := NewChatRunner(talonpath.Paths{}, nil, nil)
	_, err := runner(
		context.Background(),
		"", "sess", "run", "hi", "", nil,
		func(int, string, string, string) {},
		func(string, string, string) {},
		func(string, string, string, bool) {},
		func(seq int, kind, msg string) {
			mu.Lock()
			defer mu.Unlock()
			errCalls++
			gotKind = kind
			gotMsg = msg
		},
	)
	if err == nil {
		t.Fatal("expected the runner to return an error from empty Paths / agentID, got nil")
	}
	mu.Lock()
	defer mu.Unlock()
	if errCalls == 0 {
		t.Errorf("emitError was not called; FE would see a silent failure (err returned was %v)", err)
	}
	if gotKind == "" {
		t.Errorf("emitError kind tag is empty")
	}
	if gotMsg == "" {
		t.Errorf("emitError msg is empty")
	}
}
