//go:build !darwin

package main

import (
	"context"
	"errors"
)

// postNotification on non-darwin returns a clear error. Keeps the
// plugin binary buildable for every GOOS so the gateway's plugin
// loader doesn't need OS-specific spawn logic — the host spawns
// the plugin, the tool just fails fast on platforms where macOS
// Notification Center doesn't exist.
func postNotification(_ context.Context, _, _, _, _ string) error {
	return errors.New("mac_notify is macOS-only; this binary was built for a non-darwin OS")
}
