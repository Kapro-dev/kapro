package adapter

import (
	"fmt"
	"sort"
	"sync"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// Registry resolves public delivery adapters by BackendDriver.
type Registry struct {
	mu       sync.RWMutex
	adapters map[kaprov1alpha2.BackendDriver]Adapter
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[kaprov1alpha2.BackendDriver]Adapter)}
}

// Register adds an adapter under its Driver value.
func (r *Registry) Register(a Adapter) error {
	driver, err := validateAdapter(a)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.adapters[driver]; ok {
		return fmt.Errorf("adapter for driver %q already registered", driver)
	}
	r.adapters[driver] = a
	return nil
}

// Upsert adds or replaces an adapter and returns the previous implementation,
// when one existed.
func (r *Registry) Upsert(a Adapter) (Adapter, error) {
	driver, err := validateAdapter(a)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.adapters[driver]
	r.adapters[driver] = a
	return old, nil
}

// Unregister removes the adapter for driver.
func (r *Registry) Unregister(driver kaprov1alpha2.BackendDriver) (Adapter, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.adapters[driver]
	delete(r.adapters, driver)
	return old, ok
}

// Resolve returns the adapter registered for driver.
func (r *Registry) Resolve(driver kaprov1alpha2.BackendDriver) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[driver]
	if !ok {
		return nil, fmt.Errorf("unknown adapter driver %q", driver)
	}
	return a, nil
}

// Drivers returns registered drivers in stable lexical order.
func (r *Registry) Drivers() []kaprov1alpha2.BackendDriver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	drivers := make([]kaprov1alpha2.BackendDriver, 0, len(r.adapters))
	for driver := range r.adapters {
		drivers = append(drivers, driver)
	}
	sort.Slice(drivers, func(i, j int) bool { return drivers[i] < drivers[j] })
	return drivers
}

func validateAdapter(a Adapter) (kaprov1alpha2.BackendDriver, error) {
	if a == nil {
		return "", fmt.Errorf("adapter is nil")
	}
	driver := a.Driver()
	if driver == "" {
		return "", fmt.Errorf("adapter driver is required")
	}
	return driver, nil
}
