package main

import (
	"github.com/guygrigsby/talon/internal/plugin"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/server"
)

// agentImageProviderFactory implements server.ImageProviderFactory.
// Resolution order: loaded plugins first (any plugin whose manifest
// offers the requested image provider name), then ErrProviderUnavailable
// to signal the built-in ComfyUI fallback.
type agentImageProviderFactory struct {
	host *plugin.Host // optional; nil disables plugin-served image providers
}

// ForImage returns a PluginImageProvider when any loaded plugin advertises
// the given provider name in its manifest's OffersImageProviders list.
// Returns server.ErrProviderUnavailable otherwise so the caller falls
// through to the built-in ComfyUI path.
func (f *agentImageProviderFactory) ForImage(name string) (provider.ImageProvider, error) {
	if f.host != nil {
		if inst := f.host.ImageProviderByName(name); inst != nil {
			return plugin.NewPluginImageProvider(name, inst.Client), nil
		}
	}
	return nil, server.ErrProviderUnavailable
}
