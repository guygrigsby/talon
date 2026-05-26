package host

import (
	"fmt"
	"sync"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// Instance is a registered, initialized plugin.
type Instance struct {
	Name     string
	Manifest *pb.Manifest
	Client   pb.PluginClient
	stop     func()
}

// InstanceFields is the constructor input for NewInstance.
type InstanceFields struct {
	Name     string
	Manifest *pb.Manifest
	Client   pb.PluginClient
	Stop     func()
}

func NewInstance(f InstanceFields) *Instance {
	return &Instance{
		Name:     f.Name,
		Manifest: f.Manifest,
		Client:   f.Client,
		stop:     f.Stop,
	}
}

// Stop closes the plugin's connection and terminates owned subprocess
// resources. Idempotent.
func (i *Instance) Stop() {
	if i == nil || i.stop == nil {
		return
	}
	i.stop()
	i.stop = nil
}

// Host is the in-process registry for native gRPC plugins.
type Host struct {
	mu     sync.RWMutex
	byName map[string]*Instance
}

func NewHost() *Host {
	return &Host{byName: make(map[string]*Instance)}
}

func (h *Host) RegisterInstance(inst *Instance) error {
	if inst == nil || inst.Name == "" {
		return fmt.Errorf("plugin: RegisterInstance requires non-nil Instance with Name")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.byName[inst.Name]; exists {
		return fmt.Errorf("plugin %s: already registered", inst.Name)
	}
	h.byName[inst.Name] = inst
	return nil
}

// Unregister removes the plugin from the registry and invokes its stop
// callback. Idempotent.
func (h *Host) Unregister(name string) {
	h.mu.Lock()
	inst, ok := h.byName[name]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.byName, name)
	h.mu.Unlock()
	inst.Stop()
}

// Get returns the plugin registered under name, or nil if absent.
func (h *Host) Get(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byName[name]
}

// List returns the registered plugin names in arbitrary order.
func (h *Host) List() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.byName))
	for n := range h.byName {
		out = append(out, n)
	}
	return out
}

// ProviderByName returns the plugin instance whose manifest offers a provider
// by the given key, or nil if no loaded plugin advertises it.
func (h *Host) ProviderByName(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, inst := range h.byName {
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, ps := range inst.Manifest.OffersProviders {
			if ps.GetName() == name {
				return inst
			}
		}
	}
	return nil
}

// ChannelByName returns the plugin instance whose manifest offers a channel
// by the given name, or nil if no loaded plugin advertises it.
func (h *Host) ChannelByName(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, inst := range h.byName {
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, ch := range inst.Manifest.OffersChannels {
			if ch == name {
				return inst
			}
		}
	}
	return nil
}

// Shutdown unregisters every plugin.
func (h *Host) Shutdown() {
	h.mu.Lock()
	insts := make([]*Instance, 0, len(h.byName))
	for _, inst := range h.byName {
		insts = append(insts, inst)
	}
	h.byName = make(map[string]*Instance)
	h.mu.Unlock()
	for _, inst := range insts {
		inst.Stop()
	}
}
