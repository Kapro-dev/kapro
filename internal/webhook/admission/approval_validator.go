package admission

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	internalgate "kapro.io/kapro/internal/gate"
)

// ApprovalValidator validates Approval objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. spec.promotionRun must be non-empty.
//  2. spec.target must be non-empty.
//  3. spec.ref must be non-empty.
//  4. spec.approvedBy must be non-empty (mutator fills it from UserInfo).
//  5. metadata.name must equal "<promotionrun>-<ref>".
type ApprovalValidator struct {
	decoder admission.Decoder
}

// NewApprovalValidator returns a configured ApprovalValidator.
func NewApprovalValidator(decoder admission.Decoder) *ApprovalValidator {
	return &ApprovalValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *ApprovalValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var approval kaprov1alpha2.Approval
	if err := v.decoder.DecodeRaw(req.Object, &approval); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateApproval(&approval); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("")
}

func validateApproval(a *kaprov1alpha2.Approval) error {
	if a.Spec.PromotionRun == "" {
		return fmt.Errorf("approval.spec.promotionRun must be non-empty")
	}
	if a.Spec.Target == "" {
		return fmt.Errorf("approval.spec.target must be non-empty")
	}
	if a.Spec.Ref == "" {
		return fmt.Errorf("approval.spec.ref must be non-empty")
	}
	if a.Spec.ApprovedBy == "" {
		return fmt.Errorf("approval.spec.approvedBy must be non-empty")
	}
	expectedName := internalgate.ApprovalName(a.Spec.PromotionRun, a.Spec.Ref)
	if a.Name != expectedName {
		return fmt.Errorf("approval.metadata.name must equal %q", expectedName)
	}
	return nil
}

// ValidateApproval is an exported test helper that exposes the internal validation logic.
func ValidateApproval(a *kaprov1alpha2.Approval) error {
	return validateApproval(a)
}
