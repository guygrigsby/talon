package main

// Shared singleton + helper for resolving secret references at
// provider-construction time. Provider factories don't carry a
// context (they're called once per request and need to be cheap)
// so we use a package-scoped CachingResolver to avoid re-spawning
// the plugin process on every chat.send.

import (
	"context"
	"time"

	"github.com/guygrigsby/talon/internal/secrets"
)

// sharedSecretResolver is initialized lazily on first use.
// Process-lifetime cache means each unique reference is resolved
// once per gateway run — the chat hot path doesn't pay the
// subprocess cost.
var sharedSecretResolver = secrets.NewResolver()

// resolveSecretRef passes value through the shared resolver.
// Literal strings (no scheme) come back unchanged. References
// (op://, keychain://) are dereferenced via the matching plugin
// binary.
//
// Timeout is generous (15s) because op CLI auth + 1Password
// Connect API roundtrip can take a few seconds on cold paths.
// Subsequent calls hit the in-memory cache.
func resolveSecretRef(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !secrets.IsReference(value) {
		return value, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return sharedSecretResolver.Resolve(ctx, value)
}
