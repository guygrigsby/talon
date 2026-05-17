package pkgutil

import (
	"slices"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// MethodCapability is the gRPC-method-name -> required-capability map
// the Host-service interceptors gate against. Both the legacy and the
// native plugin hosts read from this single source so a new Host RPC
// is gated the same way regardless of which transport spawned the
// calling plugin.
//
// The map is intentionally closed: an unmapped method MUST be rejected
// by the interceptor as a programming error rather than silently
// passing ungated.
var MethodCapability = map[string]pb.Capability{
	"/talon.plugin.v1.Host/GetConfig":        pb.Capability_CAPABILITY_READ_CONFIG,
	"/talon.plugin.v1.Host/ListAgents":       pb.Capability_CAPABILITY_LIST_AGENTS,
	"/talon.plugin.v1.Host/GetAgentIdentity": pb.Capability_CAPABILITY_READ_AGENT_IDENTITY,
	"/talon.plugin.v1.Host/ListModels":       pb.Capability_CAPABILITY_LIST_MODELS,
	"/talon.plugin.v1.Host/ListSessions":     pb.Capability_CAPABILITY_LIST_SESSIONS,
	"/talon.plugin.v1.Host/GetChatHistory":   pb.Capability_CAPABILITY_READ_CHAT_HISTORY,
	"/talon.plugin.v1.Host/AppendMemory":     pb.Capability_CAPABILITY_APPEND_MEMORY,
	"/talon.plugin.v1.Host/RunSubagent":      pb.Capability_CAPABILITY_RUN_SUBAGENT,
}

// Permits reports whether m's effective capability set includes c.
// Today that's just m.Needs; the contract leaves room to layer user
// grants/denies on top in a follow-up without changing callers.
func Permits(m *pb.Manifest, c pb.Capability) bool {
	if m == nil {
		return false
	}
	return slices.Contains(m.Needs, c)
}
