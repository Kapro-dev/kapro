package server

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	argoactuator "kapro.io/kapro/internal/actuator/argo"
	directactuator "kapro.io/kapro/internal/actuator/direct"
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
		RegisterDirect(),
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

// RegisterDirect registers hub-side direct Kubernetes apply.
func RegisterDirect() ActuatorRegistrar {
	return func(_ context.Context, cc ActuatorRegistrationContext) error {
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "push/direct",
			Mode: kaprov1alpha1.DeliveryModePush,
			Capabilities: actuator.Capabilities{
				ContractVersion:          actuator.ContractVersionV1Alpha1,
				SubstrateKind:            "kubernetes-apply",
				Adapter:                  "direct",
				Runtime:                  kaprov1alpha1.SubstrateRuntimeHub,
				ExecutionModes:           []kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
				Modes:                    []kaprov1alpha1.DeliveryMode{kaprov1alpha1.DeliveryModePush},
				SupportsApply:            true,
				SupportsObserve:          true,
				SupportsRollback:         true,
				SupportsConvergence:      true,
				SupportsDelta:            true,
				SupportsSubstrateObjects: true,
				SupportsDryRun:           true,
				SupportsHubExecution:     true,
			},
			Actuator: &directactuator.Actuator{Client: cc.Manager.GetClient()},
		})
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
			Mode: kaprov1alpha1.DeliveryModePush,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateDriverFlux, kaprov1alpha1.SubstrateRuntimeHub,
				kaprov1alpha1.DeliveryModePush, false),
			Actuator: flux,
		}); err != nil {
			return err
		}
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "pull/flux",
			Mode: kaprov1alpha1.DeliveryModePull,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateDriverFlux, kaprov1alpha1.SubstrateRuntimeSpoke,
				kaprov1alpha1.DeliveryModePull, false),
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
			Mode: kaprov1alpha1.DeliveryModePull,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateDriverOCI, kaprov1alpha1.SubstrateRuntimeSpoke,
				kaprov1alpha1.DeliveryModePull, false),
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
			Mode: kaprov1alpha1.DeliveryModePush,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateDriverArgo, kaprov1alpha1.SubstrateRuntimeHub,
				kaprov1alpha1.DeliveryModePush, true),
			Actuator: &argoactuator.Actuator{Client: client},
		}); err != nil {
			return err
		}
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "pull/argo",
			Mode: kaprov1alpha1.DeliveryModePull,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateDriverArgo, kaprov1alpha1.SubstrateRuntimeSpoke,
				kaprov1alpha1.DeliveryModePull, false),
			Actuator: pull,
		})
	}
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

func builtInCapabilities(driver kaprov1alpha1.SubstrateDriver, runtime kaprov1alpha1.SubstrateRuntime, mode kaprov1alpha1.DeliveryMode, substrateObjects bool) actuator.Capabilities {
	executionMode := kaprov1alpha1.ExecutionModeHubPush
	if mode == kaprov1alpha1.DeliveryModePull {
		executionMode = kaprov1alpha1.ExecutionModeSpokePull
	}
	return actuator.Capabilities{
		ContractVersion:          actuator.ContractVersionV1Alpha1,
		SubstrateKind:            string(driver),
		Driver:                   driver,
		Adapter:                  builtInActuatorName(driver),
		Runtime:                  runtime,
		ExecutionModes:           []kaprov1alpha1.ExecutionMode{executionMode},
		Modes:                    []kaprov1alpha1.DeliveryMode{mode},
		SupportsApply:            true,
		SupportsObserve:          true,
		SupportsRollback:         true,
		SupportsConvergence:      true,
		SupportsDelta:            true,
		SupportsSubstrateObjects: substrateObjects,
		SupportsHubExecution:     executionMode == kaprov1alpha1.ExecutionModeHubPush,
		SupportsSpokeExecution:   executionMode == kaprov1alpha1.ExecutionModeSpokePull,
	}
}

func builtInActuatorName(driver kaprov1alpha1.SubstrateDriver) string {
	switch driver {
	case kaprov1alpha1.SubstrateDriverArgo:
		return "argo"
	case kaprov1alpha1.SubstrateDriverFlux:
		return "flux"
	case kaprov1alpha1.SubstrateDriverOCI:
		return "oci"
	default:
		return string(driver)
	}
}
