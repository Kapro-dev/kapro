package actuator

import (
	"fmt"
	"sync"
)

// Registry resolves KAI implementations by type name.
type Registry struct {
	mu        sync.RWMutex
	actuators map[string]Actuator
}

// NewRegistry returns a new, empty actuator Registry.
func NewRegistry() *Registry {
	return &Registry{actuators: make(map[string]Actuator)}
}

// Register adds an actuator under the given name.
func (r *Registry) Register(name string, a Actuator) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.actuators[name]; ok {
		return fmt.Errorf("actuator %q already registered", name)
	}
	r.actuators[name] = a
	return nil
}

// Upsert adds or replaces an actuator under the given name and returns the
// previous implementation, when one existed.
func (r *Registry) Upsert(name string, a Actuator) Actuator {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.actuators[name]
	r.actuators[name] = a
	return old
}

// Unregister removes an actuator by name and returns the previous
// implementation, when one existed.
func (r *Registry) Unregister(name string) (Actuator, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.actuators[name]
	delete(r.actuators, name)
	return old, ok
}

// Resolve returns the actuator registered under the given name.
func (r *Registry) Resolve(name string) (Actuator, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.actuators[name]
	if !ok {
		return nil, fmt.Errorf("unknown actuator type %q", name)
	}
	return a, nil
}
