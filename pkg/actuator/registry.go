package actuator

import (
	"fmt"
	"sync"
)

// Registry maps actuator type names (e.g. "flux", "kserve") to Actuator implementations.
// It is the runtime resolution layer: Environment.spec.actuator.type → Actuator.
//
// This is analogous to Kubernetes' container runtime registry — the engine doesn't
// hard-code Docker or containerd; it resolves from the node config at apply time.
type Registry struct {
	mu    sync.RWMutex
	impls map[string]Actuator
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{impls: make(map[string]Actuator)}
}

// Register adds an Actuator implementation for the given type name.
// Returns an error if the same type is already registered.
func (r *Registry) Register(typeName string, a Actuator) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.impls[typeName]; exists {
		return fmt.Errorf("actuator type %q already registered", typeName)
	}
	r.impls[typeName] = a
	return nil
}

// Resolve returns the Actuator for the given type, or an error if not registered.
func (r *Registry) Resolve(typeName string) (Actuator, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.impls[typeName]
	if !ok {
		return nil, fmt.Errorf("no actuator registered for type %q — available: %v", typeName, r.keys())
	}
	return a, nil
}

func (r *Registry) keys() []string {
	keys := make([]string, 0, len(r.impls))
	for k := range r.impls {
		keys = append(keys, k)
	}
	return keys
}
