package config

import "strings"

// ReloadClass classifies how a gateway picks up a change to a config path.
//
// talon never auto-restarts or auto-watches; reload is always explicit. The
// class controls only the hint we surface after a `config set` so the user
// knows whether they need to do anything.
type ReloadClass int

const (
	// ReloadNextRPC: the gateway re-reads this on the next RPC. No
	// action needed beyond the file write.
	ReloadNextRPC ReloadClass = iota
	// ReloadHUP: the gateway picks this up on SIGHUP. Reserved — no
	// paths are classified here yet because the embedded gateway still
	// needs explicit SIGHUP handlers.
	ReloadHUP
	// ReloadRestart: the value is consumed at gateway startup
	// (listener bind, plugin loader, etc.) and requires a full
	// restart to take effect.
	ReloadRestart
)

// String returns the lowercase class name as accepted by --reload.
func (c ReloadClass) String() string {
	switch c {
	case ReloadHUP:
		return "hup"
	case ReloadRestart:
		return "restart"
	default:
		return "next-rpc"
	}
}

// ParseReloadClass parses --reload values. Empty string returns ok=false so
// callers can treat it as "use the registry default".
func ParseReloadClass(s string) (ReloadClass, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, false
	case "next-rpc", "nextrpc", "next":
		return ReloadNextRPC, true
	case "hup", "sighup":
		return ReloadHUP, true
	case "restart":
		return ReloadRestart, true
	default:
		return 0, false
	}
}

// Hint returns a one-line user-facing message describing what (if anything)
// the user needs to do for this class. path is interpolated for context.
func (c ReloadClass) Hint(path string) string {
	switch c {
	case ReloadRestart:
		return "restart the gateway to apply (" + path + " is consumed at startup)"
	case ReloadHUP:
		return "send SIGHUP to the running gateway to apply " + path + " (or restart)"
	default:
		return "applies on the gateway's next request — no restart needed"
	}
}

// ClassifyReload returns the reload class for a config path. Unknown paths
// default to ReloadNextRPC: most config consumers re-read per request, so
// the optimistic default is correct more often than not. Use the explicit
// ReloadRestart list (below) for fields the gateway only reads at startup.
func ClassifyReload(segments []string) ReloadClass {
	if len(segments) == 0 {
		return ReloadNextRPC
	}
	p := SegPath(segments)

	// Exact-path restart entries.
	switch p {
	case "gateway.port",
		"gateway.bind",
		"gateway.auth.mode",
		"gateway.auth.token",
		"gateway.auth.password":
		return ReloadRestart
	}
	// Prefix-matched restart entries.
	restartPrefixes := []string{
		"gateway.tailscale.",
		"gateway.controlUi.",
		"plugins.deny",
		"plugins.load.paths",
		"skills.",
		// memory.* (enabled/path/model/recall.min_score) is read once
		// in buildMemorySidecar at gateway startup; changes need a
		// restart to rebuild the store + recaller.
		"memory.",
	}
	for _, prefix := range restartPrefixes {
		if p == strings.TrimSuffix(prefix, ".") || strings.HasPrefix(p, prefix) {
			return ReloadRestart
		}
	}
	// plugins.entries.<name>.{enabled,cmd,args} are consumed at gateway
	// startup during plugin spawn. Deeper paths (e.g. .config.*) are
	// per-plugin runtime config and can be hot-reloaded next-rpc.
	if len(segments) == 4 && segments[0] == "plugins" && segments[1] == "entries" {
		switch segments[3] {
		case "enabled", "cmd", "args":
			return ReloadRestart
		}
	}
	// HUP entries: none yet (Phase 2).
	return ReloadNextRPC
}
