package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestPromotionReconcilerCreatesNewRunWhenImmutableSpecChanges(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(2, nil)
	oldSpec := promotionRunSpecFixture("v1", false)
	newSpec := promotionRunSpecFixture("v2", false)
	existing := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: promotionRunName(promotion, oldSpec)}, Spec: oldSpec}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, existing).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var run kaprov1alpha1.PromotionRun
	if err := c.Get(ctx, client.ObjectKey{Name: promotionRunName(promotion, newSpec)}, &run); err != nil {
		t.Fatalf("expected new PromotionRun for changed immutable spec: %v", err)
	}
	if run.Spec.Version != "v2" {
		t.Fatalf("run version = %q", run.Spec.Version)
	}
}

func TestPromotionReconcilerPatchesSameRunForMutableSpecChanges(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(2, nil)
	promotion.Spec.Suspended = true
	oldSpec := promotionRunSpecFixture("v2", false)
	existing := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: promotionRunName(promotion, oldSpec)}, Spec: oldSpec}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, existing).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
	if !runs.Items[0].Spec.Suspended {
		t.Fatalf("same PromotionRun was not patched with suspended=true")
	}
}

func TestPromotionReconcilerAdoptsLegacyRunForSameImmutableSpec(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(2, nil)
	spec := promotionRunSpecFixture("v2", false)
	legacy := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: legacyPromotionRunName(promotion)},
		Spec:       spec,
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, legacy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
	if runs.Items[0].Name != legacyPromotionRunName(promotion) {
		t.Fatalf("expected legacy PromotionRun to be adopted, got %q", runs.Items[0].Name)
	}
	if runs.Items[0].Spec.Suspended {
		t.Fatalf("legacy PromotionRun should not be suspended when immutable spec matches")
	}
}

func TestPromotionReconcilerSuspendsLegacyRunBeforeNewImmutableSpec(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(2, nil)
	legacy := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: legacyPromotionRunName(promotion)},
		Spec:       promotionRunSpecFixture("v1", false),
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, legacy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var gotLegacy kaprov1alpha1.PromotionRun
	if err := c.Get(ctx, client.ObjectKey{Name: legacyPromotionRunName(promotion)}, &gotLegacy); err != nil {
		t.Fatal(err)
	}
	if !gotLegacy.Spec.Suspended {
		t.Fatalf("legacy PromotionRun should be suspended before creating replacement")
	}
	var newRun kaprov1alpha1.PromotionRun
	if err := c.Get(ctx, client.ObjectKey{Name: promotionRunName(promotion, promotionRunSpecFixture("v2", false))}, &newRun); err != nil {
		t.Fatalf("expected new digest PromotionRun: %v", err)
	}
}

func TestPromotionReconcilerBlocksMissingPolicyAndSuspendsExistingRuns(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{{Name: "signed-artifacts"}})
	promotion.Status.ActiveRun = legacyPromotionRunName(promotion)
	promotion.Status.LastRun = legacyPromotionRunName(promotion)
	existing := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: legacyPromotionRunName(promotion)},
		Spec:       promotionRunSpecFixture("v2", false),
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, existing).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
	if !runs.Items[0].Spec.Suspended {
		t.Fatalf("existing PromotionRun was not suspended")
	}
	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if got.Status.Phase != kaprov1alpha1.PromotionPhaseFailed || cond == nil || cond.Reason != "PromotionPolicyNotFound" {
		t.Fatalf("status phase=%q condition=%+v", got.Status.Phase, cond)
	}
}

func TestPromotionReconcilerEnforcesCELPolicy(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{{Name: "prod-only"}})
	promotion.Labels = map[string]string{"env": "prod"}
	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-only"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "prod-label",
				Expression: `promotion.labels.env == "prod" && promotion.version == "v2"`,
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, policy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
}

func TestPromotionReconcilerBlocksFailingCELPolicy(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{{Name: "prod-only"}})
	promotion.Labels = map[string]string{"env": "staging"}
	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-only"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "prod-label",
				Expression: `promotion.labels.env == "prod"`,
				Message:    "promotion must target prod",
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, policy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	stalled := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if got.Status.Phase != kaprov1alpha1.PromotionPhaseFailed {
		t.Fatalf("phase = %q", got.Status.Phase)
	}
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "PromotionPolicyDenied" {
		t.Fatalf("Ready condition = %+v", ready)
	}
	if stalled == nil || stalled.Status != metav1.ConditionTrue || stalled.Reason != "PromotionPolicyDenied" {
		t.Fatalf("Stalled condition = %+v", stalled)
	}
}

func TestPromotionReconcilerContinuesFailingCELPolicyWhenConfigured(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{{Name: "advisory"}})
	promotion.Labels = map[string]string{"env": "staging"}
	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "advisory"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			OnFailure: "continue",
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "prod-label",
				Expression: `promotion.labels.env == "prod"`,
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, policy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
}

func TestPromotionReconcilerAuditsFailingCELPolicy(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{{Name: "audit-only"}})
	promotion.Labels = map[string]string{"env": "staging"}
	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "audit-only"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Mode: "audit",
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "prod-label",
				Expression: `promotion.labels.env == "prod"`,
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, policy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
}

// TestPromotionReconcilerCarriesAuditViolationsThroughDenial guards the bug
// where audit violations collected from earlier policies were dropped if a
// later enforce-mode policy returned a terminal denial — the denying decision
// carries an empty AuditViolations slice, which would silently clear the
// PolicyAuditViolation condition. The runtime now copies the accumulated
// audit violations onto the returned decision so status reflects both the
// blocking enforce policy AND the audit-mode shadow blocks.
func TestPromotionReconcilerCarriesAuditViolationsThroughDenial(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{
		{Name: "audit-shadow"},
		{Name: "enforce-block"},
	})
	promotion.Labels = map[string]string{"env": "staging"}
	auditPolicy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "audit-shadow"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Mode: "audit",
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "would-have-blocked",
				Expression: `promotion.labels.env == "prod"`,
			}},
		},
	}
	enforcePolicy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "enforce-block"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "blocking",
				Expression: `promotion.labels.env == "prod"`,
				Message:    "must run in prod",
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, auditPolicy, enforcePolicy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "PromotionPolicyDenied" {
		t.Fatalf("Ready condition = %+v", ready)
	}
	audit := apimeta.FindStatusCondition(got.Status.Conditions, "PolicyAuditViolation")
	if audit == nil || audit.Status != metav1.ConditionTrue {
		t.Fatalf("PolicyAuditViolation condition should be True on the denied Promotion, got %+v", audit)
	}
	if !strings.Contains(audit.Message, "audit-shadow") {
		t.Fatalf("PolicyAuditViolation message should reference audit-shadow policy, got %q", audit.Message)
	}
}

// TestPromotionReconcilerCarriesAuditViolationsThroughEarlyReturn covers the
// same audit-carryforward contract for the early-return paths
// (InvalidPromotionPolicyRef / PromotionPolicyNotFound /
// InvalidPromotionPolicySelector) that don't go through evaluatePromotionPolicy.
// Without the deny() helper that stamps audit violations, the not-found path
// returns an empty decision and the prior audit-mode shadow block disappears.
func TestPromotionReconcilerCarriesAuditViolationsThroughEarlyReturn(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{
		{Name: "audit-shadow"},
		{Name: "does-not-exist"},
	})
	promotion.Labels = map[string]string{"env": "staging"}
	auditPolicy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "audit-shadow"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Mode: "audit",
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "would-have-blocked",
				Expression: `promotion.labels.env == "prod"`,
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, auditPolicy).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Reason != "PromotionPolicyNotFound" {
		t.Fatalf("Ready condition = %+v", ready)
	}
	audit := apimeta.FindStatusCondition(got.Status.Conditions, "PolicyAuditViolation")
	if audit == nil || audit.Status != metav1.ConditionTrue || !strings.Contains(audit.Message, "audit-shadow") {
		t.Fatalf("PolicyAuditViolation condition should survive early-return denial, got %+v", audit)
	}
}

func TestPromotionPolicyFreezeWindowBlocksPromotion(t *testing.T) {
	promotion := promotionFixture(1, nil)
	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "freeze"},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			FreezeWindows: []kaprov1alpha1.AgentTimeWindow{{
				Timezone:   "UTC",
				DaysOfWeek: []string{"Monday"},
				StartTime:  "09:00",
				EndTime:    "17:00",
			}},
		},
	}

	decision := evaluatePromotionPolicy(policy, promotion, time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC))
	if decision.Allowed || decision.Reason != "FreezeWindowActive" || decision.RequeueAfter == 0 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPromotionPolicyReconcilerSetsReadyStatus(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-only", Generation: 1},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			CEL: []kaprov1alpha1.CELPolicyRule{{
				Name:       "prod-label",
				Expression: `promotion.labels.env == "prod"`,
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.PromotionPolicy{}).
		WithObjects(policy).
		Build()
	r := &PromotionPolicyReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "prod-only"}}); err != nil {
		t.Fatal(err)
	}

	var got kaprov1alpha1.PromotionPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "prod-only"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "PolicyReady" {
		t.Fatalf("Ready condition = %+v", cond)
	}
}

func TestPromotionPolicyReconcilerRejectsUnsupportedVerification(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "verify", Generation: 1},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Verification: &kaprov1alpha1.VerificationGateSpec{},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.PromotionPolicy{}).
		WithObjects(policy).
		Build()
	r := &PromotionPolicyReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "verify"}}); err != nil {
		t.Fatal(err)
	}

	var got kaprov1alpha1.PromotionPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "verify"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "UnsupportedVerification" {
		t.Fatalf("Ready condition = %+v", cond)
	}
}

func promotionFixture(generation int64, policies []corev1.LocalObjectReference) *kaprov1alpha1.Promotion {
	return &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Generation: generation},
		Spec: kaprov1alpha1.PromotionSpec{
			Version: "v2",
			PromotionPlans: []kaprov1alpha1.PromotionPlanRef{{
				Name:          "staging",
				PromotionPlan: "default",
			}},
			Policies: policies,
		},
	}
}

func promotionRunSpecFixture(version string, suspended bool) kaprov1alpha1.PromotionRunSpec {
	return kaprov1alpha1.PromotionRunSpec{
		Version: version,
		PromotionPlans: []kaprov1alpha1.PromotionPlanRef{{
			Name:          "staging",
			PromotionPlan: "default",
		}},
		Suspended: suspended,
	}
}

func newPromotionTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
