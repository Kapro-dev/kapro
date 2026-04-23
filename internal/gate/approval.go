package gate

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApprovalGate blocks a target rollout until a matching Approval object exists on
// the control plane. The Approval is matched by:
//
//   - Approval.Spec.Kind == "Sync"
//   - Approval.Spec.Target == Request.Context.Target
//   - Approval.Spec.Release == Request.Context.ReleaseRef
//
// When Approval.Spec.Bypass == true the gate passes immediately regardless of
// other conditions (used for P0 hotfix escalations).
//
// The gate reads Approval objects via the control-plane client (they are not
// forwarded to workload clusters). The sync controller's RBAC already
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
	if req.Context == nil {
		return Result{}, fmt.Errorf("ApprovalGate.Evaluate: context is nil")
	}

	var approvalList kaprov1alpha1.ApprovalList
	if err := g.Client.List(ctx, &approvalList, client.MatchingLabels{
		"kapro.io/release": req.Context.ReleaseRef,
		"kapro.io/target":  req.Context.Target,
	}); err != nil {
		return Result{}, fmt.Errorf("list approvals: %w", err)
	}

	for i := range approvalList.Items {
		approval := &approvalList.Items[i]
		if !isMatchingApproval(approval, req.Context) {
			continue
		}

		if approval.Spec.Bypass {
			return Result{
				Phase:   kaprov1alpha1.GatePhasePassed,
				Message: fmt.Sprintf("approval bypassed by %s (bypass=true)", approval.Spec.ApprovedBy),
			}, nil
		}

		return Result{
			Phase:   kaprov1alpha1.GatePhasePassed,
			Message: fmt.Sprintf("approved by %s: %s", approval.Spec.ApprovedBy, approval.Spec.Comment),
		}, nil
	}

	return Result{
		Phase:      kaprov1alpha1.GatePhaseInconclusive,
		Message:    fmt.Sprintf("waiting for Approval for release=%s target=%s", req.Context.ReleaseRef, req.Context.Target),
		RetryAfter: "30s",
	}, nil
}

func isMatchingApproval(approval *kaprov1alpha1.Approval, gateCtx *Context) bool {
	return approval.Spec.Kind == kaprov1alpha1.ApprovalKindSync &&
		approval.Spec.Target == gateCtx.Target &&
		approval.Spec.Release == gateCtx.ReleaseRef
}
