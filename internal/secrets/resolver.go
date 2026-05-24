// Package secrets — runtime resolution of secret references.
//
// On-disk config holds NON-SECRET references like:
//
//	"botToken":  "op://Personal/talon-telegram/credential"
//	"auth":      {"token": "keychain://talon-gateway-token"}
//	"apiKey":    "op://Work/openai/api-key"
//
// At read-time, callers route these through Resolve() which dispatches
// on the scheme prefix:
//
//	op://...        → talon-op-plugin (process-isolated; needs `op` CLI)
//	keychain://...  → inlined call to `security find-generic-password` (macOS-only, no helper binary)
//	(no scheme)     → returned verbatim (literal value, back-compat)
//
// Resolved values are cached in-memory for the lifetime of the
// process — secrets don't usually rotate within a single gateway
// run, and skipping the shell-out on every chat.send keeps the hot
// path clean.
//
// The 1Password CLI authenticates via OP_SERVICE_ACCOUNT_TOKEN; we
// pull that from the macOS keychain (or env) at process start —
// the keychain entry name defaults to talon.opAccessToken and is
// configurable via gateway.secrets.opAccessTokenKeychain.

package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var _ = time.Now // imported for future per-resolution timeouts

// Resolver looks up the cleartext value behind a reference. Returns
// the input verbatim for non-references (treats "abc123" as a
// literal value, the back-compat case).
type Resolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// Reference parses a reference string into its scheme + target.
// Empty Scheme means the input was a literal value (no scheme
// prefix) — caller should pass it through unchanged.
type Reference struct {
	Scheme string // "op" | "keychain" | "" (literal)
	Target string // scheme-specific (e.g. "Personal/foo/credential")
	Raw    string // the original input
}

// ParseReference splits s into a Reference. Any string that
// doesn't match a known scheme is treated as a literal — that's
// the back-compat path: existing plaintext config keeps working
// without migration.
func ParseReference(s string) Reference {
	for _, scheme := range []string{"op", "keychain"} {
		prefix := scheme + "://"
		if strings.HasPrefix(s, prefix) {
			return Reference{
				Scheme: scheme,
				Target: strings.TrimPrefix(s, prefix),
				Raw:    s,
			}
		}
	}
	return Reference{Scheme: "", Target: s, Raw: s}
}

// IsReference returns true when s starts with a known scheme.
// Callers use this to decide whether `talon secrets ls` should
// show the value as "ref" vs "literal".
func IsReference(s string) bool {
	return ParseReference(s).Scheme != ""
}

// CachingResolver wraps a slow resolver with a process-lifetime
// in-memory cache. Hits avoid the shell-out per chat invocation.
// Misses populate the cache on first successful resolution.
//
// No TTL: secrets get re-read only when talon restarts. If a token
// rotates while the gateway runs, the user has to bounce — same
// failure mode as plaintext config today.
type CachingResolver struct {
	upstream Resolver
	mu       sync.RWMutex
	cache    map[string]string
}

func NewCachingResolver(upstream Resolver) *CachingResolver {
	return &CachingResolver{
		upstream: upstream,
		cache:    map[string]string{},
	}
}

func (r *CachingResolver) Resolve(ctx context.Context, ref string) (string, error) {
	r.mu.RLock()
	if v, ok := r.cache[ref]; ok {
		r.mu.RUnlock()
		return v, nil
	}
	r.mu.RUnlock()

	v, err := r.upstream.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.cache[ref] = v
	r.mu.Unlock()
	return v, nil
}

// dispatchResolver is the production resolver. Routes by scheme to
// the appropriate plugin binary; literal values (no scheme) pass
// through unchanged for back-compat with plaintext config.
//
// Plugin binaries follow the talon-<scheme>-plugin naming
// convention. Found via $PATH lookup first, then falling back to
// /usr/local/bin/talon-<scheme>-plugin (the docker-image install
// path used by the existing telegram + deepseek plugins).
//
// Plugin contract — minimal CLI shape rather than gRPC, since
// secret resolution is one-shot at startup / channel-init and
// doesn't need a long-lived subprocess:
//
//	talon-<scheme>-plugin <ref>  →  prints cleartext to stdout, exits 0
//	                            →  on failure, prints reason to stderr, non-zero exit
//
// The cleaner gRPC plugin protocol (used by talon-telegram-plugin
// for live channel work) is overkill for this. If we ever need
// streaming secret rotation or capability gating, lift these into
// the proper protocol then.
type dispatchResolver struct{}

// NewResolver returns a default-configured Resolver. Wraps a
// dispatch resolver in a process-lifetime cache.
func NewResolver() Resolver {
	return NewCachingResolver(&dispatchResolver{})
}

func (r *dispatchResolver) Resolve(ctx context.Context, ref string) (string, error) {
	parsed := ParseReference(ref)
	switch parsed.Scheme {
	case "":
		return parsed.Target, nil
	case "keychain":
		// Inlined (see keychain.go) so one-binary installs work.
		return resolveKeychainRef(ctx, parsed.Target)
	case "op":
		// Still plugin-routed — op has bootstrap state (service-
		// account token lookup) that earns process isolation.
		return runSecretPlugin(ctx, parsed.Scheme, parsed.Raw)
	default:
		return "", fmt.Errorf("secrets: unknown scheme %q in %q", parsed.Scheme, ref)
	}
}

// runSecretPlugin invokes talon-<scheme>-plugin with the full
// reference (preserving the scheme prefix so the plugin stays
// self-contained — it could in principle handle multiple schemes
// in one binary). Returns the stdout, trimmed.
func runSecretPlugin(ctx context.Context, scheme, ref string) (string, error) {
	binary := "talon-" + scheme + "-plugin"
	path, err := exec.LookPath(binary)
	if err != nil {
		// Three fallback locations, in order:
		//   1. Sibling of the running talon binary. Lets a dev
		//      running ./bin/talon find ./bin/talon-keychain-plugin
		//      without dropping ./bin into $PATH.
		//   2. /usr/local/bin — docker-image install location.
		//   3. /opt/homebrew/bin — Apple Silicon Homebrew default.
		for _, candidate := range pluginSearchPaths(binary) {
			if _, statErr := os.Stat(candidate); statErr == nil {
				path = candidate
				err = nil
				break
			}
		}
		if err != nil {
			return "", fmt.Errorf("secrets: %s not found on $PATH or any known install location (build with `make build`, then place on $PATH or alongside the talon binary)", binary)
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, ref)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", binary, ref, err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// --- helpers --------------------------------------------------------------

// ResolveOrLiteral is a convenience wrapper used by call sites that
// don't yet thread a Resolver through. Constructs a default
// resolver per call (uncached) — fine for one-shot reads at
// startup; long-running paths should use a single shared Resolver.
func ResolveOrLiteral(ctx context.Context, ref string) (string, error) {
	return NewResolver().Resolve(ctx, ref)
}

// pluginSearchPaths returns the fallback locations to probe when a
// secret plugin binary isn't on $PATH. Two dev-mode layouts are
// covered:
//
//	./bin/talon            with helper at ./bin/talon-keychain-plugin   (canonical make build output)
//	./talon                with helper at ./bin/talon-keychain-plugin   (project-root convenience copy)
//
// Plus the conventional install dirs. Failures from os.Executable()
// are tolerated — we just skip those probes and fall through.
func pluginSearchPaths(binary string) []string {
	out := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		// Same dir as the running binary (./bin/talon → ./bin/talon-keychain-plugin).
		out = append(out, filepath.Join(dir, binary))
		// bin/ subdir of the running binary's dir (./talon → ./bin/talon-keychain-plugin).
		out = append(out, filepath.Join(dir, "bin", binary))
	}
	out = append(out,
		"/usr/local/bin/"+binary,
		"/opt/homebrew/bin/"+binary,
	)
	return out
}

// silence unused-import linter when only some helpers are used.
var _ = errors.New
