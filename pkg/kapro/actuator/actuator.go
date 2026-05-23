// Package actuator defines the public Kapro actuator/substrate SDK.
//
// This is the stable Go import path for in-process delivery substrates:
//
//	kapro.io/kapro/pkg/kapro/actuator
package actuator

import (
	"context"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const ContractVersionV1Alpha1 = "v1alpha1"

// ApplyRequest carries everything an actuator needs to apply a version.
type ApplyRequest struct {
	Cluster         *kaprov1alpha2.Cluster
	Version         string
	PreviousVersion string
	AppKey          string
}

// DeltaApplyRequest carries a map of appKey to version for multi-artifact
// delta delivery.
type DeltaApplyRequest struct {
	Cluster         *kaprov1alpha2.Cluster
	DesiredVersions map[string]string
}

// Actuator is KAI: the Kapro Actuator Interface.
type Actuator interface {
	Apply(ctx context.Context, req ApplyRequest) error
	IsConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, version, appKey string) (bool, error)
	Rollback(ctx context.Context, cluster *kaprov1alpha2.Cluster, previousVersion, appKey string) error
	ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error)
	IsAllConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) (bool, error)
}

// BackendObjectReporter is an optional actuator extension that reports the
// backend-native objects expected to converge for a target rollout.
type BackendObjectReporter interface {
	BackendObjects(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) ([]kaprov1alpha2.BackendObjectStatus, error)
}

// Capabilities describes which part of the actuator contract a substrate
// implements and how it maps to Backend.spec fields.
type Capabilities struct {
	ContractVersion string
	Driver          kaprov1alpha2.BackendDriver
	Adapter         string
	Runtime         kaprov1alpha2.BackendRuntime
	Modes           []kaprov1alpha2.DeliveryMode

	SupportsApply          bool
	SupportsObserve        bool
	SupportsRollback       bool
	SupportsConvergence    bool
	SupportsDelta          bool
	SupportsTwoPhase       bool
	SupportsBackendObjects bool
	SupportsDryRun         bool
}

// Normalize returns a copy with stable defaults applied.
func (c Capabilities) Normalize() Capabilities {
	if c.ContractVersion == "" {
		c.ContractVersion = ContractVersionV1Alpha1
	}
	if c.Runtime == "" {
		c.Runtime = kaprov1alpha2.BackendRuntimeBoth
	}
	if c.Adapter == "" {
		c.Adapter = string(c.Driver)
	}
	return c
}

// SupportsMode reports whether the capabilities include the given delivery
// mode. An empty mode list means the registration did not publish mode
// metadata.
func (c Capabilities) SupportsMode(mode kaprov1alpha2.DeliveryMode) bool {
	for _, candidate := range c.Modes {
		if candidate == mode {
			return true
		}
	}
	return false
}

// Substrate is an actuator that can also describe its backend capabilities.
type Substrate interface {
	Actuator
	Capabilities() Capabilities
}
