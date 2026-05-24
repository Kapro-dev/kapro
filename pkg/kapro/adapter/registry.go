package adapter

import (
	"fmt"
	"sort"
	"sync"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// Registry resolves public delivery adapters by open substrate kind.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register adds an adapter under its SubstrateKind value.
func (r *Registry) Register(a Adapter) error {
	kind, err := validateAdapter(a)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.adapters[string(kind)]; ok {
		return fmt.Errorf("adapter for substrate kind %q already registered", kind)
	}
	r.adapters[string(kind)] = a
	return nil
}

// Upsert adds or replaces an adapter and returns the previous implementation,
// when one existed.
func (r *Registry) Upsert(a Adapter) (Adapter, error) {
	kind, err := validateAdapter(a)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.adapters[string(kind)]
	r.adapters[string(kind)] = a
	return old, nil
}

// Unregister removes the adapter for substrate kind.
func (r *Registry) Unregister(kind kaprov1alpha1.SubstrateKind) (Adapter, bool) {
	return r.UnregisterKind(string(kind))
}

// UnregisterKind removes the adapter for an open substrate kind.
func (r *Registry) UnregisterKind(kind string) (Adapter, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.adapters[kind]
	delete(r.adapters, kind)
	return old, ok
}

// Resolve returns the adapter registered for substrate kind.
func (r *Registry) Resolve(kind kaprov1alpha1.SubstrateKind) (Adapter, error) {
	return r.ResolveKind(string(kind))
}

// ResolveKind returns the adapter registered for an open substrate kind.
func (r *Registry) ResolveKind(kind string) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[kind]
	if !ok {
		return nil, fmt.Errorf("unknown adapter substrate %q", kind)
	}
	return a, nil
}

// SubstrateKinds returns registered substrate kinds in stable lexical order.
func (r *Registry) SubstrateKinds() []kaprov1alpha1.SubstrateKind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]kaprov1alpha1.SubstrateKind, 0, len(r.adapters))
	for kind := range r.adapters {
		kinds = append(kinds, kaprov1alpha1.SubstrateKind(kind))
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// Kinds returns registered open substrate kinds in stable lexical order.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]string, 0, len(r.adapters))
	for kind := range r.adapters {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

func validateAdapter(a Adapter) (kaprov1alpha1.SubstrateKind, error) {
	if a == nil {
		return "", fmt.Errorf("adapter is nil")
	}
	kind := a.SubstrateKind()
	if kind == "" {
		return "", fmt.Errorf("adapter substrate kind is required")
	}
	return kind, nil
}
