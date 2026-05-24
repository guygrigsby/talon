//go:build !darwin

package macopen

import (
	"context"
	"fmt"
	"runtime"
)

// runOpen on non-darwin builds returns a clear error rather than
// silently no-op'ing. The plugin still compiles on every OS so
// `go test ./...` works on CI Linux runners, but agents that
// invoke it elsewhere see why the call failed.
func runOpen(_ context.Context, _ []string) error {
	return fmt.Errorf("mac_open is macOS-only (current: %s)", runtime.GOOS)
}
