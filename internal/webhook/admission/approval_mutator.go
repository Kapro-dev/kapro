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
// Security contract: spec.approvedBy is overwritten with the Kubernetes
// UserInfo.Username from the admission request, UNLESS the caller is the
// exact trusted service account (the webhook server's SA). This prevents
// users and arbitrary in-cluster SAs from forging approver identities.
//
// The trusted SA is injected at construction time so it adapts to any
// namespace or service account name (Helm installs, custom namespaces, etc.).
//
// For human approvals via the webhook URL, the webhook server creates the
// Approval with approvedBy from the HMAC-signed token (carrying the SSO identity).
// For direct kubectl approval, approvedBy is set to the kubectl user's identity.
type ApprovalMutator struct {
	decoder           admission.Decoder
	trustedServiceAcc string
}

// NewApprovalMutator returns a configured ApprovalMutator.
// trustedSA is the full Kubernetes username of the operator's service account
// (e.g. "system:serviceaccount:kapro-system:kapro-operator"). Only this SA
// is allowed to supply a pre-filled spec.approvedBy from HMAC-signed tokens.
func NewApprovalMutator(decoder admission.Decoder, trustedSA string) *ApprovalMutator {
	return &ApprovalMutator{decoder: decoder, trustedServiceAcc: trustedSA}
}

func validateApprovalBypassComment(approval *kaprov1alpha1.Approval) admission.Response {
	if approval != nil && approval.Spec.Bypass && approval.Spec.Comment == "" {
		return admission.Denied("approval.spec.bypass requires a non-empty spec.comment (P0 justification)")
	}
	return admission.Allowed("")
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
		// Only the operator's own SA is trusted to supply approvedBy
		// (it comes from the HMAC-signed token with the human's SSO identity).
		// All other callers — including other service accounts — get overwritten.
		if req.UserInfo.Username != m.trustedServiceAcc || approval.Spec.ApprovedBy == "" {
			approval.Spec.ApprovedBy = req.UserInfo.Username
		}
		// bypass=true is a privileged escape hatch (P0 hotfix). Require a non-empty
		// comment so there is always an audit trail in the object.
		if resp := validateApprovalBypassComment(&approval); !resp.Allowed {
			return resp
		}

	case admissionv1.Update:
		var old kaprov1alpha1.Approval
		if err := m.decoder.DecodeRaw(req.OldObject, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if resp := validateApprovalBypassComment(&approval); !resp.Allowed {
			return resp
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
