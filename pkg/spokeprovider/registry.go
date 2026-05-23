package spokeprovider

import (
	"fmt"
	"sync"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// Registry resolves Provider implementations by BackendDriver.
//
// Shape mirrors pkg/actuator.Registry on purpose: hub-side actuators and
// spoke-side providers face opposite directions but follow the same
// register/resolve discipline so out-of-tree code can reuse the idiom.
type Registry struct {
	mu            sync.RWMutex
	providers     map[kaprov1alpha2.BackendDriver]Provider
	registrations map[kaprov1alpha2.BackendDriver]Registration
}

// NewRegistry returns an empty Registry ready to accept Register calls.
func NewRegistry() *Registry {
	return &Registry{
		providers:     make(map[kaprov1alpha2.BackendDriver]Provider),
		registrations: make(map[kaprov1alpha2.BackendDriver]Registration),
	}
}

// Registration binds one backend driver to a provider implementation and its
// KSP metadata.
type Registration struct {
	Driver       kaprov1alpha2.BackendDriver
	Capabilities Capabilities
	Provider     Provider
}

// Register adds a Provider under the given driver. Returns an error if the
// driver is empty or already registered. Use Upsert when replacement is the
// intent — Register fails loudly on accidental duplicate registration to
// surface wire-up bugs at startup, not at the first reconcile.
func (r *Registry) Register(driver kaprov1alpha2.BackendDriver, p Provider) error {
	driver, reg, err := normalizeRegistration(Registration{Driver: driver, Provider: p})
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[driver]; ok {
		return fmt.Errorf("provider for driver %q already registered", driver)
	}
	r.providers[driver] = p
	r.registrations[driver] = reg
	return nil
}

// RegisterRegistration adds a provider registration with explicit metadata.
func (r *Registry) RegisterRegistration(reg Registration) error {
	driver, normalized, err := normalizeRegistration(reg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[driver]; ok {
		return fmt.Errorf("provider for driver %q already registered", driver)
	}
	r.providers[driver] = normalized.Provider
	r.registrations[driver] = normalized
	return nil
}

// Upsert adds or replaces the Provider for driver and returns the previous
// implementation when one existed. Rejects empty driver and nil provider
// for the same reason Register does — a nil entry would let Resolve return
// (nil, nil) and panic the dispatcher.
func (r *Registry) Upsert(driver kaprov1alpha2.BackendDriver, p Provider) (Provider, error) {
	driver, reg, err := normalizeRegistration(Registration{Driver: driver, Provider: p})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.providers[driver]
	r.providers[driver] = p
	r.registrations[driver] = reg
	return old, nil
}

// UpsertRegistration adds or replaces a provider registration and returns the
// previous implementation when one existed.
func (r *Registry) UpsertRegistration(reg Registration) (Provider, error) {
	driver, normalized, err := normalizeRegistration(reg)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.providers[driver]
	r.providers[driver] = normalized.Provider
	r.registrations[driver] = normalized
	return old, nil
}

// Unregister removes the provider for driver and returns the previous
// implementation plus an ok flag indicating whether one existed.
func (r *Registry) Unregister(driver kaprov1alpha2.BackendDriver) (Provider, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.providers[driver]
	delete(r.providers, driver)
	delete(r.registrations, driver)
	return old, ok
}

// Resolve returns the Provider for driver or an error naming the unknown
// driver. The error is human-readable and goes straight into
// FleetCluster.status.delivery[app].lastError when an unknown driver is
// referenced — keep it stable for SRE grep recipes.
func (r *Registry) Resolve(driver kaprov1alpha2.BackendDriver) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[driver]
	if !ok {
		return nil, fmt.Errorf("unknown backend driver %q", driver)
	}
	return p, nil
}

// Registration returns provider metadata for driver.
func (r *Registry) Registration(driver kaprov1alpha2.BackendDriver) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.registrations[driver]
	return reg, ok
}

func normalizeRegistration(reg Registration) (kaprov1alpha2.BackendDriver, Registration, error) {
	if reg.Provider == nil {
		return "", Registration{}, fmt.Errorf("provider is nil")
	}
	driver := reg.Driver
	if driver == "" {
		driver = reg.Provider.Driver()
	}
	if driver == "" {
		return "", Registration{}, fmt.Errorf("driver is required")
	}
	reg.Driver = driver
	if capabilitiesEmpty(reg.Capabilities) {
		reg.Capabilities = reg.Provider.Capabilities()
	}
	reg.Capabilities = reg.Capabilities.Normalize()
	if reg.Capabilities.Driver == "" {
		reg.Capabilities.Driver = driver
	}
	if reg.Capabilities.Driver != driver {
		return "", Registration{}, fmt.Errorf("capabilities driver %q does not match registration driver %q", reg.Capabilities.Driver, driver)
	}
	if reg.Capabilities.ContractVersion != ContractVersionV1Alpha1 {
		return "", Registration{}, fmt.Errorf("unsupported provider contract version %q", reg.Capabilities.ContractVersion)
	}
	return driver, reg, nil
}

func capabilitiesEmpty(c Capabilities) bool {
	return c.ContractVersion == "" &&
		c.Driver == "" &&
		!c.SupportsReconcile &&
		!c.SupportsObserve &&
		!c.SupportsApply &&
		!c.SupportsDryRun
}
