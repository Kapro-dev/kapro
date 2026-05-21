package admission

import (
	"context"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// FleetClusterValidator validates Cluster objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. delivery.mode and delivery.backendRef must be set.
//  2. built-in Flux profiles must include the backend-specific parameter needed
//     by the selected mode.
//  3. When Reader is non-nil, the Backend named by delivery.backendRef
//     must exist AND have status.Ready=True. This closes the gap where a
//     Cluster could be admitted referencing a missing or NotReady backend.
type FleetClusterValidator struct {
	decoder admission.Decoder
	// Reader is used to look up Backend objects for reference resolution.
	// When nil, only syntactic checks run (preserves prior behavior for unit
	// tests that construct the validator without a manager).
	Reader client.Reader
}

// NewFleetClusterValidator returns a configured FleetClusterValidator. The
// reader is optional; pass mgr.GetClient() in production to enable
// Backend reference resolution, or nil for syntactic-only validation.
func NewFleetClusterValidator(decoder admission.Decoder, reader client.Reader) *FleetClusterValidator {
	return &FleetClusterValidator{decoder: decoder, Reader: reader}
}

// Handle implements admission.Handler.
func (v *FleetClusterValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var mc kaprov1alpha2.Cluster
	if err := v.decoder.DecodeRaw(req.Object, &mc); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateFleetCluster(&mc); err != nil {
		return admission.Denied(err.Error())
	}
	if v.Reader != nil {
		if err := validateFleetClusterBackendRef(ctx, v.Reader, &mc); err != nil {
			return admission.Denied(err.Error())
		}
	}
	return admission.Allowed("")
}

// validateFleetClusterBackendRef rejects Clusters whose delivery.backendRef
// names a Backend that does not exist or is not Ready. It is intentionally
// strict so misconfigurations surface at admission time rather than reconcile.
func validateFleetClusterBackendRef(ctx context.Context, reader client.Reader, mc *kaprov1alpha2.Cluster) error {
	name := mc.Spec.Delivery.BackendRef
	if name == "" {
		return nil // syntactic validator already rejected the empty case
	}
	var profile kaprov1alpha2.Backend
	if err := reader.Get(ctx, client.ObjectKey{Name: name}, &profile); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("cluster.spec.delivery.backendRef=%q: backend not found; create the Backend CR before referencing it", name)
		}
		return fmt.Errorf("cluster.spec.delivery.backendRef=%q: lookup failed: %w", name, err)
	}
	if !profile.Status.Ready {
		return fmt.Errorf("cluster.spec.delivery.backendRef=%q: backend is not Ready; resolve its Ready condition before referencing", name)
	}
	return nil
}

// ValidateFleetClusterBackendRef is an exported test helper for the new
// reference-resolution check.
func ValidateFleetClusterBackendRef(ctx context.Context, reader client.Reader, mc *kaprov1alpha2.Cluster) error {
	return validateFleetClusterBackendRef(ctx, reader, mc)
}

func validateFleetCluster(mc *kaprov1alpha2.Cluster) error {
	return validateActuator(mc)
}

func validateActuator(mc *kaprov1alpha2.Cluster) error {
	act := mc.Spec.Delivery
	if act.Mode == "" {
		return fmt.Errorf("cluster.spec.delivery.mode must be set")
	}
	if act.BackendRef == "" {
		return fmt.Errorf("cluster.spec.delivery.backendRef must be set")
	}
	switch act.BackendRef {
	case "flux":
		if act.Mode == kaprov1alpha2.DeliveryModePull && act.Param("ociRepository", "") == "" {
			return fmt.Errorf("cluster.spec.delivery.parameters.ociRepository must be set when mode=pull and backendRef=flux")
		}
		if act.Mode == kaprov1alpha2.DeliveryModePush && act.Param("resourceSet", "") == "" {
			return fmt.Errorf("cluster.spec.delivery.parameters.resourceSet must be set when mode=push and backendRef=flux")
		}
	case "argo":
		return nil
	default:
		return nil
	}
	return nil
}

// ValidateFleetCluster is an exported test helper that exposes the internal validation logic.
func ValidateFleetCluster(mc *kaprov1alpha2.Cluster) error {
	return validateFleetCluster(mc)
}
