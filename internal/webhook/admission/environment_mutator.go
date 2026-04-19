package admission

import (
	"context"
	"encoding/json"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const labelAccelerator = "kapro.io/accelerator"

// EnvironmentMutator is a mutating admission webhook for Environment objects.
//
// Topology label injection: when spec.topology.accelerator is set, the webhook
// ensures metadata.labels["kapro.io/accelerator"] mirrors that value so that
// Stage.selector.matchLabels can target accelerator types (H100, A100, etc.)
// without requiring manual label management.
//
// Removing spec.topology.accelerator removes the managed label on the next
// CREATE/UPDATE — labels set by users directly are never touched.
type EnvironmentMutator struct {
	decoder admission.Decoder
}

// NewEnvironmentMutator returns a configured EnvironmentMutator.
func NewEnvironmentMutator(decoder admission.Decoder) *EnvironmentMutator {
	return &EnvironmentMutator{decoder: decoder}
}

// Handle implements admission.Handler.
func (m *EnvironmentMutator) Handle(_ context.Context, req admission.Request) admission.Response {
	var env kaprov1alpha1.Environment
	if err := m.decoder.DecodeRaw(req.Object, &env); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if env.Labels == nil {
		env.Labels = map[string]string{}
	}

	if env.Spec.Topology != nil && env.Spec.Topology.Accelerator != "" {
		env.Labels[labelAccelerator] = env.Spec.Topology.Accelerator
	} else {
		// Remove the managed label when the topology field is cleared.
		delete(env.Labels, labelAccelerator)
	}

	marshaled, err := json.Marshal(env)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaled)
}
