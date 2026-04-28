// Package netutil holds small networking helpers used across talon.
//
// The first one (rewriteLoopback) bridges the "I configured
// localhost:1234 but the gateway is in a container" footgun: every
// caller that resolves a user-supplied URL targeting a local service
// (LM Studio, ollama, …) needs the same logic, and the chat factory
// + the models.list discovery path are not in the same internal
// package.
package netutil

import (
	"net/url"
	"os"
	"sync"
)

var (
	inContainerOnce sync.Once
	inContainer     bool
)

// RunningInContainer reports whether this process is in a Docker /
// OCI container by checking for the /.dockerenv flag file (Docker)
// and /run/.containerenv (Podman). Cached after first call — file
// presence doesn't change for the life of the process.
func RunningInContainer() bool {
	inContainerOnce.Do(func() {
		for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
			if _, err := os.Stat(p); err == nil {
				inContainer = true
				return
			}
		}
	})
	return inContainer
}

// RewriteLoopbackForContainer swaps localhost / 127.0.0.1 / ::1 in
// rawURL for "host.docker.internal" when the process is in a
// container. No-op outside containers, or when the URL targets a
// non-loopback host.
func RewriteLoopbackForContainer(rawURL string) string {
	return RewriteLoopback(rawURL, RunningInContainer())
}

// RewriteLoopback is the pure function — takes the inContainer bool
// explicitly so tests can exercise both branches without filesystem
// stubbing.
func RewriteLoopback(rawURL string, inContainer bool) string {
	if !inContainer {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return rawURL
	}
	port := u.Port()
	if port != "" {
		u.Host = "host.docker.internal:" + port
	} else {
		u.Host = "host.docker.internal"
	}
	return u.String()
}
