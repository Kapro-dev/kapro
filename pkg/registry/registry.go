// Package registry provides a generic, thread-safe named registry for Kapro
// extension points.
//
// # Design
//
// Kapro exposes two pluggable extension interfaces today:
//
//   - actuator.Actuator — how a version is applied to a target cluster
//     (pkg/actuator).
//   - gate.Gate — an evaluator that answers "is it safe to advance?"
//     (pkg/gate).
//
// Both follow the same lifecycle:
//
//  1. Startup: operator binary registers implementations by name.
//  2. Runtime: controllers resolve implementations by name — actuators by
//     MemberCluster.spec.actuator.type, gates by Stage.gate.* or
//     GateTemplate.spec.name.
//  3. Reconcile: implementation is called through its interface.
//
// The Registry[T] type provides this lifecycle for any interface T. Per-type
// registries (actuator.Registry, gate.Registry) embed Registry[T] rather than
// duplicating the sync.RWMutex + map pattern.
//
// # Analogy
//
// Registry[T] is to Kapro what the kubelet plugin registry is to Kubernetes:
// a named, runtime-resolved dispatch table for extension implementations.
// Just as the kubelet resolves a CSI driver by name in a PersistentVolume spec,
// Kapro resolves an Actuator by name in MemberCluster.spec.actuator.type.
//
// # Usage
//
//	// At startup (main.go or init()):
//	reg := pkgregistry.New[actuator.Actuator]("actuator")
//	reg.MustRegister("flux", &fluxactuator.FluxActuator{Client: mgr.GetClient()})
//
//	// At reconcile time (controller):
//	impl, err := reg.Resolve(mc.Spec.Actuator.Type)
//
// # Stability
//
// This package is stable as of v1alpha1. The Registry[T] type and all exported
// methods have backwards-compatibility guarantees within a major version.
package registry

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is a generic, thread-safe named registry for extension implementations.
//
// T is the extension interface type (e.g. actuator.Actuator, gate.Gate).
// The zero value is not usable — use New[T] to construct.
//
// All methods are safe for concurrent use.
type Registry[T any] struct {
	mu           sync.RWMutex
	impls        map[string]T
	registryName string // used in error messages, e.g. "actuator"
}

// New returns a new, empty Registry for type T.
// registryName is used in error messages to identify which registry the error
// originated from (e.g. "actuator", "gate").
func New[T any](registryName string) *Registry[T] {
	return &Registry[T]{
		impls:        make(map[string]T),
		registryName: registryName,
	}
}

// Register adds impl to the registry under typeName.
//
// Returns ErrAlreadyRegistered if typeName is already registered.
// typeName must be non-empty.
func (r *Registry[T]) Register(typeName string, impl T) error {
	if typeName == "" {
		return fmt.Errorf("%s registry: typeName must not be empty", r.registryName)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.impls[typeName]; exists {
		return fmt.Errorf("%s registry: %q already registered — call Unregister first or check for duplicate init() calls",
			r.registryName, typeName)
	}
	r.impls[typeName] = impl
	return nil
}

// MustRegister adds impl to the registry under typeName.
// Panics if typeName is already registered.
// Use in init() blocks and main() where a registration conflict is always
// a programming error that should crash the process immediately.
func (r *Registry[T]) MustRegister(typeName string, impl T) {
	if err := r.Register(typeName, impl); err != nil {
		panic(fmt.Sprintf("kapro/registry: MustRegister: %v", err)) // intentional startup panic — duplicate registration is always a programming error
	}
}

// Resolve returns the implementation registered under typeName.
//
// Returns ErrNotFound if typeName is not registered.
// The returned value is the exact value passed to Register — callers must not
// modify it in place.
func (r *Registry[T]) Resolve(typeName string) (T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	impl, ok := r.impls[typeName]
	if !ok {
		var zero T
		return zero, fmt.Errorf("%s registry: %q is not registered — registered types: %v",
			r.registryName, typeName, r.sortedKeys())
	}
	return impl, nil
}

// Has returns true if typeName is registered.
func (r *Registry[T]) Has(typeName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.impls[typeName]
	return ok
}

// Names returns a sorted slice of all registered type names.
// The slice is a snapshot — it is not updated when new types are registered.
func (r *Registry[T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sortedKeys()
}

// All returns a copy of all registered implementations keyed by type name.
// The map is a snapshot — modifications do not affect the registry.
func (r *Registry[T]) All() map[string]T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]T, len(r.impls))
	for k, v := range r.impls {
		out[k] = v
	}
	return out
}

// Unregister removes the implementation registered under typeName.
// Returns false if typeName was not registered.
// Prefer Register-once patterns; Unregister is provided for testing only.
func (r *Registry[T]) Unregister(typeName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.impls[typeName]
	if ok {
		delete(r.impls, typeName)
	}
	return ok
}

// Len returns the number of registered implementations.
func (r *Registry[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.impls)
}

// sortedKeys returns sorted keys without acquiring the lock.
// Callers must hold r.mu.RLock() or r.mu.Lock().
func (r *Registry[T]) sortedKeys() []string {
	keys := make([]string, 0, len(r.impls))
	for k := range r.impls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
