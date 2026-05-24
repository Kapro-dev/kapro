// Package actuator defines the public Kapro actuator/substrate SDK.
//
// This is the stable Go import path for in-process delivery substrates:
//
//	kapro.io/kapro/pkg/kapro/actuator
package actuator

import (
	"context"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

const ContractVersionV1Alpha1 = "v1alpha1"

// ApplyRequest carries everything an actuator needs to apply a version.
type ApplyRequest struct {
	Cluster         *kaprov1alpha1.Cluster
	Version         string
	PreviousVersion string
	AppKey          string
}

// DeltaApplyRequest carries a map of appKey to version for multi-artifact
// delta delivery.
type DeltaApplyRequest struct {
	Cluster         *kaprov1alpha1.Cluster
	DesiredVersions map[string]string
}

// Actuator is KAI: the Kapro Actuator Interface.
type Actuator interface {
	Apply(ctx context.Context, req ApplyRequest) error
	IsConverged(ctx context.Context, cluster *kaprov1alpha1.Cluster, version, appKey string) (bool, error)
	Rollback(ctx context.Context, cluster *kaprov1alpha1.Cluster, previousVersion, appKey string) error
	ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error)
	IsAllConverged(ctx context.Context, cluster *kaprov1alpha1.Cluster, desiredVersions map[string]string) (bool, error)
}

// SubstrateObjectReporter is an optional actuator extension that reports the
// substrate-native objects expected to converge for a target rollout.
type SubstrateObjectReporter interface {
	SubstrateObjects(ctx context.Context, cluster *kaprov1alpha1.Cluster, desiredVersions map[string]string) ([]kaprov1alpha1.SubstrateObjectStatus, error)
}

// Capabilities describes which part of the actuator contract a substrate
// implements and how it maps to Substrate.spec fields.
type Capabilities struct {
	ContractVersion string
	SubstrateKind   kaprov1alpha1.SubstrateKind
	Actuator        string
	ExecutionScope  kaprov1alpha1.ExecutionScope
	ExecutionModes  []kaprov1alpha1.ExecutionMode
	Modes           []kaprov1alpha1.DeliveryMode

	SupportsApply            bool
	SupportsObserve          bool
	SupportsRollback         bool
	SupportsConvergence      bool
	SupportsDelta            bool
	SupportsTwoPhase         bool
	SupportsSubstrateObjects bool
	SupportsDryRun           bool
	SupportsHubExecution     bool
	SupportsSpokeExecution   bool
	SupportsExternalPull     bool
}

// Normalize returns a copy with stable defaults applied.
func (c Capabilities) Normalize() Capabilities {
	if c.ContractVersion == "" {
		c.ContractVersion = ContractVersionV1Alpha1
	}
	if c.ExecutionScope == "" {
		c.ExecutionScope = kaprov1alpha1.ExecutionScopeBoth
	}
	if c.Actuator == "" {
		c.Actuator = string(c.SubstrateKind)
	}
	for _, mode := range c.ExecutionModes {
		switch mode {
		case kaprov1alpha1.ExecutionModeHubPush:
			c.SupportsHubExecution = true
		case kaprov1alpha1.ExecutionModeSpokePull:
			c.SupportsSpokeExecution = true
		case kaprov1alpha1.ExecutionModeExternalPull:
			c.SupportsExternalPull = true
		}
	}
	return c
}

// SupportsMode reports whether the capabilities include the given delivery
// mode. An empty mode list means the registration did not publish mode
// metadata.
func (c Capabilities) SupportsMode(mode kaprov1alpha1.DeliveryMode) bool {
	for _, candidate := range c.Modes {
		if candidate == mode {
			return true
		}
	}
	return false
}

// SupportsExecutionMode reports whether this actuator supports a canonical
// substrate execution mode. Empty execution metadata means the registration did
// not publish topology metadata.
func (c Capabilities) SupportsExecutionMode(mode kaprov1alpha1.ExecutionMode) bool {
	switch mode {
	case kaprov1alpha1.ExecutionModeHubPush:
		return c.SupportsHubExecution
	case kaprov1alpha1.ExecutionModeSpokePull:
		return c.SupportsSpokeExecution
	case kaprov1alpha1.ExecutionModeExternalPull:
		return c.SupportsExternalPull
	default:
		return false
	}
}

// Substrate is an actuator that can also describe its substrate capabilities.
type Substrate interface {
	Actuator
	Capabilities() Capabilities
}
