package admission

import (
	"context"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// FleetClusterValidator validates Cluster objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. delivery.mode and delivery.substrateRef must be set.
//  2. When Reader is non-nil, the Substrate named by delivery.substrateRef
//     is checked opportunistically and reported as a warning if absent.
//     Existence and readiness are controller lifecycle state, not admission
//     requirements, so GitOps/bootstrap flows can apply Substrate and Cluster
//     objects together in any order and let controllers converge them.
type FleetClusterValidator struct {
	decoder admission.Decoder
	// Reader is used to look up Substrate objects for reference resolution.
	// When nil, only syntactic checks run (preserves prior behavior for unit
	// tests that construct the validator without a manager).
	Reader client.Reader
}

// NewFleetClusterValidator returns a configured FleetClusterValidator. The
// reader is optional; pass mgr.GetClient() in production to enable
// Substrate reference resolution, or nil for syntactic-only validation.
func NewFleetClusterValidator(decoder admission.Decoder, reader client.Reader) *FleetClusterValidator {
	return &FleetClusterValidator{decoder: decoder, Reader: reader}
}

// Handle implements admission.Handler.
func (v *FleetClusterValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var mc kaprov1alpha1.Cluster
	if err := v.decoder.DecodeRaw(req.Object, &mc); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateFleetCluster(&mc); err != nil {
		return admission.Denied(err.Error())
	}
	if v.Reader != nil {
		warnings, err := validateFleetClusterSubstrateRef(ctx, v.Reader, &mc)
		if err != nil {
			return admission.Denied(err.Error())
		}
		if len(warnings) > 0 {
			return admission.Allowed("").WithWarnings(warnings...)
		}
	}
	return admission.Allowed("")
}

// validateFleetClusterSubstrateRef reports missing or temporarily unreadable
// Substrates as warnings, not denials. Admission stays structural; the Target
// reconciler waits for Substrate existence/readiness before it applies.
func validateFleetClusterSubstrateRef(ctx context.Context, reader client.Reader, mc *kaprov1alpha1.Cluster) ([]string, error) {
	name := mc.Spec.Delivery.SubstrateRef
	if name == "" {
		return nil, nil // syntactic validator already rejected the empty case
	}
	var profile kaprov1alpha1.Substrate
	if err := reader.Get(ctx, client.ObjectKey{Name: name}, &profile); err != nil {
		if apierrors.IsNotFound(err) {
			return []string{fmt.Sprintf("cluster.spec.delivery.substrateRef=%q: substrate not found yet; Target execution will wait for it", name)}, nil
		}
		return []string{fmt.Sprintf("cluster.spec.delivery.substrateRef=%q: substrate lookup failed; Target execution will retry: %v", name, err)}, nil
	}
	return validateResolvedSubstrateParameters(mc, profile.Spec.SubstrateKind()), nil
}

// ValidateFleetClusterSubstrateRef is an exported test helper for the new
// reference-resolution check.
func ValidateFleetClusterSubstrateRef(ctx context.Context, reader client.Reader, mc *kaprov1alpha1.Cluster) ([]string, error) {
	return validateFleetClusterSubstrateRef(ctx, reader, mc)
}

func validateFleetCluster(mc *kaprov1alpha1.Cluster) error {
	return validateActuator(mc)
}

func validateActuator(mc *kaprov1alpha1.Cluster) error {
	act := mc.Spec.Delivery
	if act.Mode == "" {
		return fmt.Errorf("cluster.spec.delivery.mode must be set")
	}
	if act.SubstrateRef == "" {
		return fmt.Errorf("cluster.spec.delivery.substrateRef must be set")
	}
	return nil
}

func validateResolvedSubstrateParameters(mc *kaprov1alpha1.Cluster, substrateKind string) []string {
	act := mc.Spec.Delivery
	switch substrateKind {
	case string(kaprov1alpha1.SubstrateDriverFlux):
		if act.Mode == kaprov1alpha1.DeliveryModePull && act.Param("ociRepository", "") == "" {
			return []string{"cluster.spec.delivery.parameters.ociRepository is required by flux pull delivery; Target execution will fail until it is set"}
		}
		if act.Mode == kaprov1alpha1.DeliveryModePush && act.Param("resourceSet", "") == "" {
			return []string{"cluster.spec.delivery.parameters.resourceSet is required by flux push delivery; Target execution will fail until it is set"}
		}
	}
	return nil
}

// ValidateFleetCluster is an exported test helper that exposes the internal validation logic.
func ValidateFleetCluster(mc *kaprov1alpha1.Cluster) error {
	return validateFleetCluster(mc)
}
