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
			Mode: kaprov1alpha1.SubstrateModePush,
			Capabilities: actuator.Capabilities{
				ContractVersion:          actuator.ContractVersionV1Alpha1,
				SubstrateKind:            "kubernetes-apply",
				Actuator:                 "direct",
				ExecutionScope:           kaprov1alpha1.ExecutionScopeHub,
				ExecutionModes:           []kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
				Modes:                    []kaprov1alpha1.SubstrateMode{kaprov1alpha1.SubstrateModePush},
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
			Mode: kaprov1alpha1.SubstrateModePush,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateKindFlux, kaprov1alpha1.ExecutionScopeHub,
				kaprov1alpha1.SubstrateModePush, false),
			Actuator: flux,
		}); err != nil {
			return err
		}
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "pull/flux",
			Mode: kaprov1alpha1.SubstrateModePull,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateKindFlux, kaprov1alpha1.ExecutionScopeSpoke,
				kaprov1alpha1.SubstrateModePull, false),
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
			Mode: kaprov1alpha1.SubstrateModePull,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateKindOCI, kaprov1alpha1.ExecutionScopeSpoke,
				kaprov1alpha1.SubstrateModePull, false),
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
			Mode: kaprov1alpha1.SubstrateModePush,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateKindArgo, kaprov1alpha1.ExecutionScopeHub,
				kaprov1alpha1.SubstrateModePush, true),
			Actuator: &argoactuator.Actuator{Client: client},
		}); err != nil {
			return err
		}
		return registerBuiltInActuator(cc.Registry, actuator.Registration{
			Name: "pull/argo",
			Mode: kaprov1alpha1.SubstrateModePull,
			Capabilities: builtInCapabilities(kaprov1alpha1.SubstrateKindArgo, kaprov1alpha1.ExecutionScopeSpoke,
				kaprov1alpha1.SubstrateModePull, false),
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

func builtInCapabilities(driver kaprov1alpha1.SubstrateKind, runtime kaprov1alpha1.ExecutionScope, mode kaprov1alpha1.SubstrateMode, substrateObjects bool) actuator.Capabilities {
	executionMode := kaprov1alpha1.ExecutionModeHubPush
	if mode == kaprov1alpha1.SubstrateModePull {
		executionMode = kaprov1alpha1.ExecutionModeSpokePull
	}
	return actuator.Capabilities{
		ContractVersion:          actuator.ContractVersionV1Alpha1,
		SubstrateKind:            driver,
		Actuator:                 builtInActuatorName(driver),
		ExecutionScope:           runtime,
		ExecutionModes:           []kaprov1alpha1.ExecutionMode{executionMode},
		Modes:                    []kaprov1alpha1.SubstrateMode{mode},
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

func builtInActuatorName(driver kaprov1alpha1.SubstrateKind) string {
	switch driver {
	case kaprov1alpha1.SubstrateKindArgo:
		return "argo"
	case kaprov1alpha1.SubstrateKindFlux:
		return "flux"
	case kaprov1alpha1.SubstrateKindOCI:
		return "oci"
	default:
		return string(driver)
	}
}
