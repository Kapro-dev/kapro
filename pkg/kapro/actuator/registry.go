package actuator

import (
	"fmt"
	"sort"
	"sync"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// Registration binds one runtime registry key to an actuator implementation.
type Registration struct {
	Name         string
	Mode         kaprov1alpha2.DeliveryMode
	Capabilities Capabilities
	Actuator     Actuator
}

// RegistryKey returns the runtime key used to resolve this registration.
func (r Registration) RegistryKey() string {
	if r.Name != "" {
		return r.Name
	}
	mode := r.Mode
	if mode == "" && len(r.Capabilities.Modes) > 0 {
		mode = r.Capabilities.Modes[0]
	}
	adapter := r.Capabilities.Adapter
	if adapter == "" {
		adapter = string(r.Capabilities.Driver)
	}
	return string(mode) + "/" + adapter
}

// Registry resolves actuator implementations by runtime key and retains
// capability metadata for substrate discovery.
type Registry struct {
	mu            sync.RWMutex
	actuators     map[string]Actuator
	registrations map[string]Registration
}

// NewRegistry returns a new, empty actuator Registry.
func NewRegistry() *Registry {
	return &Registry{
		actuators:     make(map[string]Actuator),
		registrations: make(map[string]Registration),
	}
}

// Register adds an actuator under the given runtime key.
func (r *Registry) Register(name string, a Actuator) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.actuators[name]; ok {
		return fmt.Errorf("actuator %q already registered", name)
	}
	r.actuators[name] = a
	r.registrations[name] = Registration{
		Name:         name,
		Capabilities: Capabilities{ContractVersion: ContractVersionV1Alpha1},
		Actuator:     a,
	}
	return nil
}

// RegisterRegistration adds a substrate registration under its runtime key.
func (r *Registry) RegisterRegistration(reg Registration) error {
	key, normalized, err := normalizeRegistration(reg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.actuators[key]; ok {
		return fmt.Errorf("actuator %q already registered", key)
	}
	r.actuators[key] = normalized.Actuator
	r.registrations[key] = normalized
	return nil
}

// Upsert adds or replaces an actuator under the given key and returns the
// previous implementation, when one existed.
func (r *Registry) Upsert(name string, a Actuator) Actuator {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.actuators[name]
	r.actuators[name] = a
	r.registrations[name] = Registration{
		Name:         name,
		Capabilities: Capabilities{ContractVersion: ContractVersionV1Alpha1},
		Actuator:     a,
	}
	return old
}

// UpsertRegistration adds or replaces a substrate registration and returns the
// previous implementation when one existed. A nil previous return value is
// ambiguous with a registration failure, so this method surfaces
// normalizeRegistration errors explicitly instead of swallowing them.
func (r *Registry) UpsertRegistration(reg Registration) (Actuator, error) {
	key, normalized, err := normalizeRegistration(reg)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.actuators[key]
	r.actuators[key] = normalized.Actuator
	r.registrations[key] = normalized
	return old, nil
}

// Unregister removes an actuator by key and returns the previous
// implementation, when one existed.
func (r *Registry) Unregister(name string) (Actuator, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.actuators[name]
	delete(r.actuators, name)
	delete(r.registrations, name)
	return old, ok
}

// Resolve returns the actuator registered under the given key.
func (r *Registry) Resolve(name string) (Actuator, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.actuators[name]
	if !ok {
		return nil, fmt.Errorf("unknown actuator type %q", name)
	}
	return a, nil
}

// Registration returns the substrate metadata registered under the given key.
func (r *Registry) Registration(name string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.registrations[name]
	return reg, ok
}

// Names returns the registered runtime keys in stable order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.actuators))
	for name := range r.actuators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalizeRegistration(reg Registration) (string, Registration, error) {
	if reg.Actuator == nil {
		return "", Registration{}, fmt.Errorf("actuator is nil")
	}
	if substrate, ok := reg.Actuator.(Substrate); ok && capabilitiesEmpty(reg.Capabilities) {
		reg.Capabilities = substrate.Capabilities()
	}
	reg.Capabilities = reg.Capabilities.Normalize()
	if reg.Mode != "" && len(reg.Capabilities.Modes) == 0 {
		reg.Capabilities.Modes = []kaprov1alpha2.DeliveryMode{reg.Mode}
	}
	key := reg.RegistryKey()
	if key == "" || key == "/" {
		return "", Registration{}, fmt.Errorf("actuator registry key is required")
	}
	// RegistryKey() falls back to "<mode>/<adapter>". When Name is empty
	// and mode is empty too, we'd produce "/argo" — a key that never
	// matches DeliverySpec.RegistryKey() at resolution time. Reject the
	// leading-slash form rather than silently registering an unreachable
	// actuator.
	if reg.Name == "" && len(key) > 0 && key[0] == '/' {
		return "", Registration{}, fmt.Errorf("actuator registration requires Name or non-empty Mode/Capabilities.Modes; got key %q", key)
	}
	reg.Name = key
	return key, reg, nil
}

func capabilitiesEmpty(c Capabilities) bool {
	return c.ContractVersion == "" &&
		c.Driver == "" &&
		c.Adapter == "" &&
		c.Runtime == "" &&
		len(c.Modes) == 0 &&
		!c.SupportsApply &&
		!c.SupportsObserve &&
		!c.SupportsRollback &&
		!c.SupportsConvergence &&
		!c.SupportsDelta &&
		!c.SupportsBackendObjects &&
		!c.SupportsDryRun
}
