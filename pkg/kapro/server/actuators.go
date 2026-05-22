package server

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	argoactuator "kapro.io/kapro/internal/actuator/argo"
	fluxopactuator "kapro.io/kapro/internal/actuator/fluxoperator"
	pullactuator "kapro.io/kapro/internal/actuator/pull"
	"kapro.io/kapro/pkg/kapro/actuator"
)

// ActuatorRegistrationContext is passed to server actuator registrar functions.
type ActuatorRegistrationContext struct {
	Manager   ctrl.Manager
	Registry  *actuator.Registry
	Log       logr.Logger
	DevMode   bool
	ShardName string
}

// ActuatorRegistrar registers one or more actuator/substrate implementations.
type ActuatorRegistrar func(context.Context, ActuatorRegistrationContext) error

// DefaultActuatorRegistrars returns the built-in reference operator actuator
// registrations.
func DefaultActuatorRegistrars() []ActuatorRegistrar {
	return []ActuatorRegistrar{
		RegisterFlux(),
		RegisterOCI(),
		RegisterArgoCD(),
	}
}

// RegisterActuator adapts a public actuator.Registration into a server
// registrar.
func RegisterActuator(reg actuator.Registration) ActuatorRegistrar {
	return func(_ context.Context, cc ActuatorRegistrationContext) error {
		if cc.Registry == nil {
			return fmt.Errorf("actuator registry is nil")
		}
		if err := cc.Registry.RegisterRegistration(reg); err != nil {
			return fmt.Errorf("register actuator %q: %w", reg.RegistryKey(), err)
		}
		return nil
	}
}

// RegisterFlux registers the built-in Flux substrates.
func RegisterFlux() ActuatorRegistrar {
	return func(_ context.Context, cc ActuatorRegistrationContext) error {
		client := cc.Manager.GetClient()
		flux := &fluxopactuator.FluxOperatorActuator{Client: client}
		pull := &pullactuator.PullActuator{HubClient: client}
		if err := registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "push/flux",
			Mode: kaprov1alpha2.DeliveryModePush,
			Capabilities: builtInCapabilities(kaprov1alpha2.BackendDriverFlux, kaprov1alpha2.BackendRuntimeHub,
				kaprov1alpha2.DeliveryModePush, false),
			Actuator: flux,
		}); err != nil {
			return err
		}
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "pull/flux",
			Mode: kaprov1alpha2.DeliveryModePull,
			Capabilities: builtInCapabilities(kaprov1alpha2.BackendDriverFlux, kaprov1alpha2.BackendRuntimeSpoke,
				kaprov1alpha2.DeliveryModePull, false),
			Actuator: pull,
		})
	}
}

// RegisterOCI registers the built-in pull-mode OCI Delivery Core bridge.
func RegisterOCI() ActuatorRegistrar {
	return func(_ context.Context, cc ActuatorRegistrationContext) error {
		pull := &pullactuator.PullActuator{HubClient: cc.Manager.GetClient()}
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "pull/oci",
			Mode: kaprov1alpha2.DeliveryModePull,
			Capabilities: builtInCapabilities(kaprov1alpha2.BackendDriverOCI, kaprov1alpha2.BackendRuntimeSpoke,
				kaprov1alpha2.DeliveryModePull, false),
			Actuator: pull,
		})
	}
}

// RegisterArgoCD registers the built-in Argo CD substrates.
func RegisterArgoCD() ActuatorRegistrar {
	return func(_ context.Context, cc ActuatorRegistrationContext) error {
		client := cc.Manager.GetClient()
		pull := &pullactuator.PullActuator{HubClient: client}
		if err := registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "push/argo",
			Mode: kaprov1alpha2.DeliveryModePush,
			Capabilities: builtInCapabilities(kaprov1alpha2.BackendDriverArgo, kaprov1alpha2.BackendRuntimeHub,
				kaprov1alpha2.DeliveryModePush, true),
			Actuator: &argoactuator.Actuator{Client: client},
		}); err != nil {
			return err
		}
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "pull/argo",
			Mode: kaprov1alpha2.DeliveryModePull,
			Capabilities: builtInCapabilities(kaprov1alpha2.BackendDriverArgo, kaprov1alpha2.BackendRuntimeSpoke,
				kaprov1alpha2.DeliveryModePull, false),
			Actuator: pull,
		})
	}
}

func registerActuators(ctx context.Context, registrars []ActuatorRegistrar, cc ActuatorRegistrationContext) (*actuator.Registry, error) {
	registry := actuator.NewRegistry()
	cc.Registry = registry
	for _, registrar := range registrars {
		if registrar == nil {
			continue
		}
		if err := registrar(ctx, cc); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func registerBuiltInActuator(registry *actuator.Registry, reg actuator.Registration) error {
	if registry == nil {
		return fmt.Errorf("actuator registry is nil")
	}
	if err := registry.RegisterRegistration(reg); err != nil {
		return fmt.Errorf("register %s actuator: %w", reg.RegistryKey(), err)
	}
	return nil
}

func builtInCapabilities(driver kaprov1alpha2.BackendDriver, runtime kaprov1alpha2.BackendRuntime, mode kaprov1alpha2.DeliveryMode, backendObjects bool) actuator.Capabilities {
	return actuator.Capabilities{
		ContractVersion:        actuator.ContractVersionV1Alpha1,
		Driver:                 driver,
		Adapter:                string(driver),
		Runtime:                runtime,
		Modes:                  []kaprov1alpha2.DeliveryMode{mode},
		SupportsApply:          true,
		SupportsObserve:        true,
		SupportsRollback:       true,
		SupportsConvergence:    true,
		SupportsDelta:          true,
		SupportsBackendObjects: backendObjects,
	}
}
