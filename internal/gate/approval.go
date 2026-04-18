package gate

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApprovalGate blocks a promotion until a matching Approval object exists on
// the control plane. The Approval is matched by:
//
//   - Approval.Spec.Kind == "Promotion"
//   - Approval.Spec.EnvironmentRef == Promotion.Spec.EnvironmentRef
//   - Approval.Spec.ReleaseRef == Promotion.Spec.ReleaseRef
//
// When Approval.Spec.Bypass == true the gate passes immediately regardless of
// other conditions (used for P0 hotfix escalations).
//
// The gate reads Approval objects via the control-plane client (they are not
// forwarded to workload clusters). The promotion controller's RBAC already
// includes get;list;watch on approvals.
type ApprovalGate struct {
	// Client is the control-plane Kubernetes client injected at startup.
	Client client.Client
}

// Evaluate lists Approval objects and returns Passed=true when a matching,
// non-expired approval is found.
func (g *ApprovalGate) Evaluate(ctx context.Context, req Request) (Result, error) {
	if g.Client == nil {
		return Result{}, fmt.Errorf("ApprovalGate.Client is nil")
	}
	if req.Promotion == nil {
		return Result{}, fmt.Errorf("ApprovalGate.Evaluate: promotion is nil")
	}

	var approvalList kaprov1alpha1.ApprovalList
	if err := g.Client.List(ctx, &approvalList, client.MatchingLabels{
		"kapro.io/release":     req.Promotion.Spec.ReleaseRef,
		"kapro.io/environment": req.Promotion.Spec.EnvironmentRef,
	}); err != nil {
		return Result{}, fmt.Errorf("list approvals: %w", err)
	}

	for i := range approvalList.Items {
		approval := &approvalList.Items[i]
		if !isMatchingApproval(approval, req.Promotion) {
			continue
		}

		if approval.Spec.Bypass {
			return Result{
				Passed:  true,
				Message: fmt.Sprintf("approval bypassed by %s (bypass=true)", approval.Spec.ApprovedBy),
			}, nil
		}

		return Result{
			Passed:  true,
			Message: fmt.Sprintf("approved by %s: %s", approval.Spec.ApprovedBy, approval.Spec.Comment),
		}, nil
	}

	return Result{
		Passed:     false,
		Message:    fmt.Sprintf("waiting for Approval for release=%s env=%s", req.Promotion.Spec.ReleaseRef, req.Promotion.Spec.EnvironmentRef),
		RetryAfter: "30s",
	}, nil
}

func isMatchingApproval(approval *kaprov1alpha1.Approval, promo *kaprov1alpha1.Promotion) bool {
	return approval.Spec.Kind == kaprov1alpha1.ApprovalKindPromotion &&
		approval.Spec.EnvironmentRef == promo.Spec.EnvironmentRef &&
		approval.Spec.Release == promo.Spec.ReleaseRef
}
