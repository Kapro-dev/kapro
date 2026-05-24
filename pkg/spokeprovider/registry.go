package spokeprovider

import (
	"fmt"
	"sync"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// Registry resolves Provider implementations by SubstrateKind.
//
// Shape mirrors pkg/actuator.Registry on purpose: hub-side actuators and
// spoke-side providers face opposite directions but follow the same
// register/resolve discipline so out-of-tree code can reuse the idiom.
type Registry struct {
	mu            sync.RWMutex
	providers     map[kaprov1alpha1.SubstrateKind]Provider
	registrations map[kaprov1alpha1.SubstrateKind]Registration
}

// NewRegistry returns an empty Registry ready to accept Register calls.
func NewRegistry() *Registry {
	return &Registry{
		providers:     make(map[kaprov1alpha1.SubstrateKind]Provider),
		registrations: make(map[kaprov1alpha1.SubstrateKind]Registration),
	}
}

// Registration binds one substrate kind to a provider implementation and its
// KSP metadata.
type Registration struct {
	SubstrateKind kaprov1alpha1.SubstrateKind
	Capabilities  Capabilities
	Provider      Provider
}

// Register adds a Provider under the given substrate kind. Returns an error if the
// substrate kind is empty or already registered. Use Upsert when replacement is the
// intent — Register fails loudly on accidental duplicate registration to
// surface wire-up bugs at startup, not at the first reconcile.
func (r *Registry) Register(driver kaprov1alpha1.SubstrateKind, p Provider) error {
	driver, reg, err := normalizeRegistration(Registration{SubstrateKind: driver, Provider: p})
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[driver]; ok {
		return fmt.Errorf("provider for substrate kind %q already registered", driver)
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
		return fmt.Errorf("provider for substrate kind %q already registered", driver)
	}
	r.providers[driver] = normalized.Provider
	r.registrations[driver] = normalized
	return nil
}

// Upsert adds or replaces the Provider for substrate kind and returns the previous
// implementation when one existed. Rejects empty substrate kind and nil provider
// for the same reason Register does — a nil entry would let Resolve return
// (nil, nil) and panic the dispatcher.
func (r *Registry) Upsert(driver kaprov1alpha1.SubstrateKind, p Provider) (Provider, error) {
	driver, reg, err := normalizeRegistration(Registration{SubstrateKind: driver, Provider: p})
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

// Unregister removes the provider for substrate kind and returns the previous
// implementation plus an ok flag indicating whether one existed.
func (r *Registry) Unregister(driver kaprov1alpha1.SubstrateKind) (Provider, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.providers[driver]
	delete(r.providers, driver)
	delete(r.registrations, driver)
	return old, ok
}

// Resolve returns the Provider for substrate kind or an error naming the unknown
// kind. The error is human-readable and goes straight into
// FleetCluster.status.delivery[app].lastError when an unknown substrate kind is
// referenced — keep it stable for SRE grep recipes.
func (r *Registry) Resolve(driver kaprov1alpha1.SubstrateKind) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[driver]
	if !ok {
		return nil, fmt.Errorf("unknown substrate kind %q", driver)
	}
	return p, nil
}

// Registration returns provider metadata for substrate kind.
func (r *Registry) Registration(driver kaprov1alpha1.SubstrateKind) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.registrations[driver]
	return reg, ok
}

func normalizeRegistration(reg Registration) (kaprov1alpha1.SubstrateKind, Registration, error) {
	if reg.Provider == nil {
		if reg.SubstrateKind != "" {
			return "", Registration{}, fmt.Errorf("provider for substrate kind %q is nil", reg.SubstrateKind)
		}
		return "", Registration{}, fmt.Errorf("provider is nil")
	}
	driver := reg.SubstrateKind
	providerDriver := reg.Provider.SubstrateKind()
	if providerDriver == "" {
		return "", Registration{}, fmt.Errorf("provider substrate kind is required")
	}
	if driver == "" {
		driver = providerDriver
	}
	if driver != providerDriver {
		return "", Registration{}, fmt.Errorf("provider substrate kind %q does not match registration substrate kind %q", providerDriver, driver)
	}
	if driver == "" {
		return "", Registration{}, fmt.Errorf("substrate kind is required")
	}
	reg.SubstrateKind = driver
	if capabilitiesEmpty(reg.Capabilities) {
		reg.Capabilities = reg.Provider.Capabilities()
	}
	reg.Capabilities = reg.Capabilities.Normalize()
	if reg.Capabilities.SubstrateKind == "" {
		reg.Capabilities.SubstrateKind = driver
	}
	if reg.Capabilities.SubstrateKind != driver {
		return "", Registration{}, fmt.Errorf("capabilities substrate kind %q does not match registration substrate kind %q", reg.Capabilities.SubstrateKind, driver)
	}
	if reg.Capabilities.ContractVersion != ContractVersionV1Alpha1 {
		return "", Registration{}, fmt.Errorf("unsupported provider contract version %q", reg.Capabilities.ContractVersion)
	}
	return driver, reg, nil
}

func capabilitiesEmpty(c Capabilities) bool {
	return c.ContractVersion == "" &&
		c.SubstrateKind == "" &&
		!c.SupportsReconcile &&
		!c.SupportsObserve &&
		!c.SupportsApply &&
		!c.SupportsDryRun
}
