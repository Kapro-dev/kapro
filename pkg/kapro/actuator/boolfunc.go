package actuator

import (
	"context"
	"fmt"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// BoolFunc is minimal sugar for trivial custom substrates and tests.
//
// It adapts a function returning (ok, message, error) to the full Actuator
// interface. Production actuators should implement Actuator and Capabilities
// explicitly so apply, observe, rollback, and delta semantics are clear.
type BoolFunc func(ctx context.Context, req ApplyRequest) (bool, string, error)

// NewBoolFunc returns a minimal Substrate backed by fn.
func NewBoolFunc(kind string, fn BoolFunc) Substrate {
	return boolSubstrate{
		kind: kind,
		fn:   fn,
	}
}

type boolSubstrate struct {
	kind string
	fn   BoolFunc
}

func (b boolSubstrate) Apply(ctx context.Context, req ApplyRequest) error {
	ok, msg, err := b.evaluate(ctx, req)
	if err != nil {
		return fmt.Errorf("ApplyError: %w", err)
	}
	if !ok {
		if msg == "" {
			msg = "bool actuator returned false"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func (b boolSubstrate) IsConverged(ctx context.Context, cluster *kaprov1alpha1.Cluster, version, appKey string) (bool, error) {
	ok, _, err := b.evaluate(ctx, ApplyRequest{Cluster: cluster, Version: version, AppKey: appKey})
	if err != nil {
		return false, fmt.Errorf("ObserveError: %w", err)
	}
	return ok, nil
}

func (b boolSubstrate) Rollback(context.Context, *kaprov1alpha1.Cluster, string, string) error {
	return fmt.Errorf("RollbackUnsupported: BoolFunc actuators do not support rollback")
}

func (b boolSubstrate) ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error) {
	applied := 0
	for appKey, version := range req.DesiredVersions {
		if err := b.Apply(ctx, ApplyRequest{Cluster: req.Cluster, Version: version, AppKey: appKey}); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

func (b boolSubstrate) IsAllConverged(ctx context.Context, cluster *kaprov1alpha1.Cluster, desiredVersions map[string]string) (bool, error) {
	for appKey, version := range desiredVersions {
		converged, err := b.IsConverged(ctx, cluster, version, appKey)
		if err != nil || !converged {
			return converged, err
		}
	}
	return true, nil
}

func (b boolSubstrate) Capabilities() Capabilities {
	return Capabilities{
		ContractVersion:      ContractVersionV1Alpha1,
		SubstrateKind:        kaprov1alpha1.SubstrateKind(b.kind),
		Actuator:             b.kind,
		ExecutionScope:       kaprov1alpha1.ExecutionScopeHub,
		ExecutionModes:       []kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
		Modes:                []kaprov1alpha1.DeliveryMode{kaprov1alpha1.DeliveryModePush},
		SupportsApply:        true,
		SupportsObserve:      true,
		SupportsConvergence:  true,
		SupportsDelta:        true,
		SupportsHubExecution: true,
	}.Normalize()
}

func (b boolSubstrate) evaluate(ctx context.Context, req ApplyRequest) (bool, string, error) {
	if b.fn == nil {
		return false, "", fmt.Errorf("bool actuator function is nil")
	}
	return b.fn(ctx, req)
}
