package gate

import (
	"fmt"
	"sort"
	"sync"
)

// Registry maps gate type strings to Predicate implementations.
type Registry struct {
	mu         sync.RWMutex
	predicates map[string]Predicate
	tracing    bool
}

// NewRegistry returns an empty, ready-to-use Registry that wraps registered
// predicates with OpenTelemetry tracing by default.
func NewRegistry() *Registry {
	return &Registry{predicates: make(map[string]Predicate), tracing: true}
}

// NewRegistryWithoutTracing returns an empty Registry that does not auto-wrap
// predicates. Tests and callers that already apply tracing can use this to
// avoid nested spans.
func NewRegistryWithoutTracing() *Registry {
	return &Registry{predicates: make(map[string]Predicate)}
}

// Register adds a predicate implementation under the given name.
func (r *Registry) Register(name string, p Predicate) error {
	if name == "" {
		return fmt.Errorf("predicate name is required")
	}
	if p == nil {
		return fmt.Errorf("predicate %q is nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.predicates[name]; ok {
		return fmt.Errorf("predicate %q already registered", name)
	}
	r.predicates[name] = p
	return nil
}

// Upsert adds or replaces a predicate implementation under the given name and
// returns the previous implementation, when one existed.
//
// Upsert keeps its pre-Predicate source-compatible signature. Prefer Register
// for new predicates that should fail on empty names or nil implementations.
func (r *Registry) Upsert(name string, p Predicate) Predicate {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.predicates[name]
	r.predicates[name] = p
	return old
}

// Unregister removes a predicate implementation by name and returns the previous
// implementation, when one existed.
func (r *Registry) Unregister(name string) (Predicate, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.predicates[name]
	delete(r.predicates, name)
	return old, ok
}

// MustRegister registers a predicate or panics.
func (r *Registry) MustRegister(name string, p Predicate) {
	if err := r.Register(name, p); err != nil {
		panic(err)
	}
}

// Resolve returns the predicate registered under the given name.
func (r *Registry) Resolve(name string) (Predicate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.predicates[name]
	if !ok {
		return nil, fmt.Errorf("unknown predicate %q", name)
	}
	if r.tracing {
		return WithTracing(name, p), nil
	}
	return p, nil
}

// Names returns registered predicate names in stable lexical order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.predicates))
	for name := range r.predicates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
