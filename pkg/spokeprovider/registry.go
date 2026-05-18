package spokeprovider

import (
	"fmt"
	"sync"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Registry resolves Provider implementations by BackendDriver.
//
// Shape mirrors pkg/actuator.Registry on purpose: hub-side actuators and
// spoke-side providers face opposite directions but follow the same
// register/resolve discipline so out-of-tree code can reuse the idiom.
type Registry struct {
	mu        sync.RWMutex
	providers map[kaprov1alpha1.BackendDriver]Provider
}

// NewRegistry returns an empty Registry ready to accept Register calls.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[kaprov1alpha1.BackendDriver]Provider)}
}

// Register adds a Provider under the given driver. Returns an error if the
// driver is empty or already registered. Use Upsert when replacement is the
// intent — Register fails loudly on accidental duplicate registration to
// surface wire-up bugs at startup, not at the first reconcile.
func (r *Registry) Register(driver kaprov1alpha1.BackendDriver, p Provider) error {
	if driver == "" {
		return fmt.Errorf("driver is required")
	}
	if p == nil {
		return fmt.Errorf("provider for driver %q is nil", driver)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[driver]; ok {
		return fmt.Errorf("provider for driver %q already registered", driver)
	}
	r.providers[driver] = p
	return nil
}

// Upsert adds or replaces the Provider for driver and returns the previous
// implementation when one existed. Rejects empty driver and nil provider
// for the same reason Register does — a nil entry would let Resolve return
// (nil, nil) and panic the dispatcher.
func (r *Registry) Upsert(driver kaprov1alpha1.BackendDriver, p Provider) (Provider, error) {
	if driver == "" {
		return nil, fmt.Errorf("driver is required")
	}
	if p == nil {
		return nil, fmt.Errorf("provider for driver %q is nil", driver)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.providers[driver]
	r.providers[driver] = p
	return old, nil
}

// Unregister removes the provider for driver and returns the previous
// implementation plus an ok flag indicating whether one existed.
func (r *Registry) Unregister(driver kaprov1alpha1.BackendDriver) (Provider, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.providers[driver]
	delete(r.providers, driver)
	return old, ok
}

// Resolve returns the Provider for driver or an error naming the unknown
// driver. The error is human-readable and goes straight into
// FleetCluster.status.delivery[app].lastError when an unknown driver is
// referenced — keep it stable for SRE grep recipes.
func (r *Registry) Resolve(driver kaprov1alpha1.BackendDriver) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[driver]
	if !ok {
		return nil, fmt.Errorf("unknown backend driver %q", driver)
	}
	return p, nil
}
