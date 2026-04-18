// Package admission provides Kubernetes mutating/validating admission webhooks for Kapro CRDs.
package admission

import (
	"context"
	"encoding/json"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApprovalMutator is a mutating admission webhook for Approval objects.
//
// Security contract: spec.approvedBy is ALWAYS overwritten with the Kubernetes
// UserInfo.Username from the admission request. This prevents users from creating
// Approval objects with a forged approver identity via kubectl or the API.
//
// The only trusted path to set approvedBy is the HMAC-signed webhook token
// (internal/webhook/server.go), which uses the ApprovedBy claim from the token.
// That path bypasses this webhook because the webhook server creates the Approval
// object using its own service account — the service account identity is set here.
//
// For human approvals via the webhook URL, approvedBy comes from the signed token
// (the token was minted by the notification system with the SSO identity).
// For direct kubectl approval, approvedBy is set to the kubectl user's identity.
type ApprovalMutator struct {
	decoder admission.Decoder
}

// NewApprovalMutator returns a configured ApprovalMutator.
func NewApprovalMutator(decoder admission.Decoder) *ApprovalMutator {
	return &ApprovalMutator{decoder: decoder}
}

// Handle implements admission.Handler.
// On CREATE: overwrites spec.approvedBy with the actual Kubernetes username.
// On UPDATE: rejects attempts to change approvedBy (immutable after creation).
func (m *ApprovalMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var approval kaprov1alpha1.Approval
	if err := m.decoder.DecodeRaw(req.Object, &approval); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	switch req.Operation {
	case admissionv1.Create:
		// Overwrite whatever the client sent — the real identity is in UserInfo.
		approval.Spec.ApprovedBy = req.UserInfo.Username
		// bypass=true is a privileged escape hatch (P0 hotfix). Require a non-empty
		// comment so there is always an audit trail in the object.
		if approval.Spec.Bypass && approval.Spec.Comment == "" {
			return admission.Denied("approval.spec.bypass requires a non-empty spec.comment (P0 justification)")
		}

	case admissionv1.Update:
		var old kaprov1alpha1.Approval
		if err := m.decoder.DecodeRaw(req.OldObject, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		// approvedBy is immutable once set — reject changes.
		if old.Spec.ApprovedBy != "" && approval.Spec.ApprovedBy != old.Spec.ApprovedBy {
			return admission.Denied("approval.spec.approvedBy is immutable after creation")
		}
	}

	marshaled, err := json.Marshal(approval)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaled)
}
