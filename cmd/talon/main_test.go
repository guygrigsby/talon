package main

import (
	"context"
	"errors"
	"testing"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/gateway"
)

// resetSharedRPCForTest tears down any cached connection and zeroes
// the per-test counter. Tests that touch sharedRPC must call this
// before swapping dialFn so they observe a clean slate.
func resetSharedRPCForTest(t *testing.T) {
	t.Helper()
	closeSharedRPC()
	rpcConn.mu.Lock()
	rpcConn.dialCount = 0
	rpcConn.mu.Unlock()
}

func TestSharedRPC_CachesAcrossSequentialCalls(t *testing.T) {
	resetSharedRPCForTest(t)
	prev := dialFn
	t.Cleanup(func() { dialFn = prev; closeSharedRPC() })

	// Stub the dialer so we can count invocations without spinning
	// up a real WS server. The returned *gateway.Client is unused by
	// the test (we only check dialCount), so a zero-valued pointer
	// is fine for the cache-keying contract.
	stub := &gateway.Client{}
	cfg := &config.Config{}
	dialFn = func(_ context.Context) (*gateway.Client, *config.Config, error) {
		return stub, cfg, nil
	}

	for i := 0; i < 5; i++ {
		cli, _, err := sharedRPC(t.Context())
		if err != nil {
			t.Fatalf("sharedRPC call %d: %v", i, err)
		}
		if cli != stub {
			t.Errorf("call %d returned a different client: %p (want %p)", i, cli, stub)
		}
	}
	rpcConn.mu.Lock()
	got := rpcConn.dialCount
	rpcConn.mu.Unlock()
	if got != 1 {
		t.Errorf("dialCount = %d, want 1 (sequential calls should reuse the cached connection)", got)
	}
}

func TestSharedRPC_CachesDialError(t *testing.T) {
	resetSharedRPCForTest(t)
	prev := dialFn
	t.Cleanup(func() { dialFn = prev; closeSharedRPC() })

	wantErr := errors.New("simulated dial failure")
	dialFn = func(_ context.Context) (*gateway.Client, *config.Config, error) {
		return nil, nil, wantErr
	}

	for i := 0; i < 3; i++ {
		_, _, err := sharedRPC(t.Context())
		if !errors.Is(err, wantErr) {
			t.Errorf("call %d: got %v, want %v", i, err, wantErr)
		}
	}
	rpcConn.mu.Lock()
	got := rpcConn.dialCount
	rpcConn.mu.Unlock()
	// Caching the error means a single dial + same error returned to
	// every subsequent caller. Without this, a dead gateway would
	// chew through N dial timeouts per multi-RPC command.
	if got != 1 {
		t.Errorf("dialCount = %d, want 1 (errors should also be cached so a dead gateway doesn't burn dial budget per call)", got)
	}
}

func TestCloseSharedRPC_AllowsReDial(t *testing.T) {
	resetSharedRPCForTest(t)
	prev := dialFn
	t.Cleanup(func() { dialFn = prev; closeSharedRPC() })

	calls := 0
	dialFn = func(_ context.Context) (*gateway.Client, *config.Config, error) {
		calls++
		return &gateway.Client{}, &config.Config{}, nil
	}

	if _, _, err := sharedRPC(t.Context()); err != nil {
		t.Fatal(err)
	}
	closeSharedRPC()
	if _, _, err := sharedRPC(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("dialFn called %d time(s); want 2 (close should drop the cache)", calls)
	}
}
