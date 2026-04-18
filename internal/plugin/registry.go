// Package plugin manages runtime registration of external Kapro plugins.
//
// Plugins communicate with the operator over gRPC. Each plugin registers itself
// by creating a PluginRegistration CRD in the cluster. The operator watches these
// CRDs, dials gRPC connections, and exposes bridge implementations of the KSI
// interfaces (gate.Gate, actuator.Actuator, etc.) that proxy calls over the wire.
//
// Architecture (mirrors Crossplane provider model):
//
//	PluginRegistration CRD ← plugin binary writes this on startup
//	    ↓
//	PluginRegistration controller (this package) dials gRPC
//	    ↓
//	Registry stores name → *grpc.ClientConn
//	    ↓
//	Bridge types implement gate.Gate / actuator.Actuator / provider.Connector
//	    ↓
//	Promotion / BatchRun controllers use bridges transparently
package plugin

import (
	"fmt"
	"sync"

	"google.golang.org/grpc"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Entry holds an active gRPC connection to a plugin.
type Entry struct {
	Conn       *grpc.ClientConn
	PluginType kaprov1alpha1.PluginType
	// Name is the PluginRegistration resource name — used as the lookup key for
	// bridge wiring (e.g. MetricGate.Provider == "rancher" → plugin "rancher").
	Name string
}

// Registry is a thread-safe store of active plugin connections, keyed by
// PluginRegistration name. Connections are established by the Reconciler and
// consumed by the bridge types.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry // key: PluginRegistration.Name
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]*Entry)}
}

// Register stores a new entry. Replaces any existing connection for the same name
// (Reconciler calls this on reconnect).
func (r *Registry) Register(name string, entry *Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[name] = entry
}

// Deregister removes and closes the connection for the given plugin name.
// Safe to call when the PluginRegistration is deleted.
func (r *Registry) Deregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[name]; ok {
		_ = e.Conn.Close()
		delete(r.entries, name)
	}
}

// Get returns the Entry for the given plugin name, or an error if not found.
func (r *Registry) Get(name string) (*Entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not registered (is PluginRegistration Connected?)", name)
	}
	return e, nil
}

// ByType returns all entries of the given plugin type.
// Used when the operator wants to fan-out a call to all plugins of a type.
func (r *Registry) ByType(t kaprov1alpha1.PluginType) []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Entry
	for _, e := range r.entries {
		if e.PluginType == t {
			out = append(out, e)
		}
	}
	return out
}

// Names returns all registered plugin names (for diagnostics).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.entries))
	for k := range r.entries {
		names = append(names, k)
	}
	return names
}
