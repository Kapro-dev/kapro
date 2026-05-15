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

// Replace updates an existing gate registration.
func (r *Registry) Replace(name string, g Gate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.gates[name]; !ok {
		return fmt.Errorf("gate %q is not registered", name)
	}
	r.gates[name] = g
	return nil
}

// Unregister removes a gate registration when present.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.gates, name)
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
