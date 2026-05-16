package gate

import (
	"fmt"
	"sync"
)

// Registry maps gate type strings to Gate implementations.
type Registry struct {
	mu    sync.RWMutex
	gates map[string]Gate
}

// NewRegistry returns an empty, ready-to-use gate Registry.
func NewRegistry() *Registry {
	return &Registry{gates: make(map[string]Gate)}
}

// Register adds a gate implementation under the given name.
func (r *Registry) Register(name string, g Gate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.gates[name]; ok {
		return fmt.Errorf("gate %q already registered", name)
	}
	r.gates[name] = g
	return nil
}

// Upsert adds or replaces a gate implementation under the given name and
// returns the previous implementation, when one existed.
func (r *Registry) Upsert(name string, g Gate) Gate {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.gates[name]
	r.gates[name] = g
	return old
}

// Unregister removes a gate implementation by name and returns the previous
// implementation, when one existed.
func (r *Registry) Unregister(name string) (Gate, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.gates[name]
	delete(r.gates, name)
	return old, ok
}

// MustRegister registers a gate or panics.
func (r *Registry) MustRegister(name string, g Gate) {
	if err := r.Register(name, g); err != nil {
		panic(err)
	}
}

// Resolve returns the gate registered under the given name.
func (r *Registry) Resolve(name string) (Gate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.gates[name]
	if !ok {
		return nil, fmt.Errorf("unknown gate type %q", name)
	}
	return g, nil
}
