package gate

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// ApprovalGate blocks a target rollout until a cluster-scoped Approval object
// with the deterministic name ApprovalName(promotionrun, ref) exists.
//
// When Approval.Spec.Bypass == true the gate passes regardless of the
// approvedBy value (used for P0 hotfix escalations).
//
// Identity is deterministic: one cluster-scoped Approval per (promotionrun, ref)
// pair. For target FSM approvals, ref is the stable per-target sync name
// (<promotionrun>-<promotionplanRef>-<stage>-<target>), which prevents one approval from
// being silently reused across later waiting-approval steps for the same target.
// The webhook server, the kapro CLI, and this gate all agree on the same
// object key — no label scans, no spec filtering.
type ApprovalGate struct {
	// Client is the control-plane Kubernetes client injected at startup.
	Client client.Client
}

// ApprovalName returns the canonical Approval object name for the given
// (promotionrun, ref) pair. All producers (webhook, CLI, gate) must use
// this helper so identity stays in one place.
func ApprovalName(promotionrun, ref string) string {
	return fmt.Sprintf("%s-%s", promotionrun, ref)
}

// Evaluate returns Passed when the deterministic Approval exists, and
// Inconclusive otherwise.
func (g *ApprovalGate) Evaluate(ctx context.Context, req Request) (Result, error) {
	if g.Client == nil {
		return Result{}, fmt.Errorf("ApprovalGate.Client is nil")
	}
	if req.Context == nil {
		return Result{}, fmt.Errorf("ApprovalGate.Evaluate: context is nil")
	}

	ref := req.Context.Name
	if ref == "" {
		ref = req.Context.Target
	}
	key := client.ObjectKey{Name: ApprovalName(req.Context.PromotionRunRef, ref)}
	var approval kaprov1alpha1.Approval
	if err := g.Client.Get(ctx, key, &approval); err != nil {
		if apierrors.IsNotFound(err) {
			return Result{
				Phase:      kaprov1alpha1.GatePhaseInconclusive,
				Message:    fmt.Sprintf("waiting for Approval %q", key.Name),
				RetryAfter: "30s",
				Evidence: []Evidence{{
					Type:   "approval",
					Reason: fmt.Sprintf("approval %q not found", key.Name),
				}},
			}, nil
		}
		return Result{}, fmt.Errorf("get approval %q: %w", key.Name, err)
	}

	if req.Policy != nil && req.Policy.Approval != nil && len(req.Policy.Approval.Approvers) > 0 {
		allowed := false
		for _, approver := range req.Policy.Approval.Approvers {
			if approver == approval.Spec.ApprovedBy {
				allowed = true
				break
			}
		}
		if !allowed {
			return Result{
				Phase:      kaprov1alpha1.GatePhaseFailed,
				Message:    fmt.Sprintf("approval by %s is not allowed", approval.Spec.ApprovedBy),
				RetryAfter: "0",
				Evidence: []Evidence{{
					Type:   "approval",
					Reason: "approver is not allowed by policy",
				}},
			}, nil
		}
	}

	if approval.Spec.Bypass {
		return Result{
			Phase:   kaprov1alpha1.GatePhasePassed,
			Message: fmt.Sprintf("approval bypassed by %s", approval.Spec.ApprovedBy),
			Evidence: []Evidence{{
				Type:   "approval",
				Reason: "approval bypass flag set",
			}},
		}, nil
	}
	return Result{
		Phase:   kaprov1alpha1.GatePhasePassed,
		Message: fmt.Sprintf("approved by %s: %s", approval.Spec.ApprovedBy, approval.Spec.Comment),
		Evidence: []Evidence{{
			Type:   "approval",
			Reason: "approval object exists and approver is allowed",
		}},
	}, nil
}
