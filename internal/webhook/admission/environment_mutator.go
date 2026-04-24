package admission

import (
	"context"
	"encoding/json"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const labelAccelerator = "kapro.io/accelerator"

// MemberClusterMutator is a mutating admission webhook for MemberCluster objects.
//
// Topology label injection: when spec.topology.accelerator is set, the webhook
// ensures metadata.labels["kapro.io/accelerator"] mirrors that value so that
// Stage.selector.matchLabels can target accelerator types (H100, A100, etc.)
// without requiring manual label management.
//
// Removing spec.topology.accelerator removes the managed label on the next
// CREATE/UPDATE — labels set by users directly are never touched.
type MemberClusterMutator struct {
	decoder admission.Decoder
}

// NewMemberClusterMutator returns a configured MemberClusterMutator.
func NewMemberClusterMutator(decoder admission.Decoder) *MemberClusterMutator {
	return &MemberClusterMutator{decoder: decoder}
}

// Handle implements admission.Handler.
func (m *MemberClusterMutator) Handle(_ context.Context, req admission.Request) admission.Response {
	var mc kaprov1alpha1.MemberCluster
	if err := m.decoder.DecodeRaw(req.Object, &mc); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if mc.Labels == nil {
		mc.Labels = map[string]string{}
	}

	if mc.Spec.Topology != nil && mc.Spec.Topology.Accelerator != "" {
		mc.Labels[labelAccelerator] = mc.Spec.Topology.Accelerator
	} else if req.Operation == admissionv1.Update {
		var old kaprov1alpha1.MemberCluster
		if err := m.decoder.DecodeRaw(req.OldObject, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		// Remove the label only when the webhook previously managed it.
		if old.Spec.Topology != nil &&
			old.Spec.Topology.Accelerator != "" &&
			old.Labels[labelAccelerator] == old.Spec.Topology.Accelerator {
			delete(mc.Labels, labelAccelerator)
		}
	}

	marshaled, err := json.Marshal(mc)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaled)
}
