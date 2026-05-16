package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/webhook/token"
	gatepkg "kapro.io/kapro/pkg/gate"
)

type staticGate struct {
	result gatepkg.Result
	err    error
}

func (g staticGate) Evaluate(_ context.Context, _ gatepkg.Request) (gatepkg.Result, error) {
	return g.result, g.err
}

func controllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add coordination scheme: %v", err)
	}
	return scheme
}

func TestHandleVerification_FailedResultFailsTarget(t *testing.T) {
	reg := gatepkg.NewRegistry()
	reg.MustRegister("verification", staticGate{
		result: gatepkg.Result{
			Phase:   kaprov1alpha1.GatePhaseFailed,
			Message: "signature verification failed",
		},
	})

	r := &PromotionTargetReconciler{
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	promotionrun := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"},
	}
	target := &kaprov1alpha1.TargetStatus{
		PromotionRunRef:  "rel-1",
		Target:           "cluster-a",
		PromotionPlanRef: "wave-1",
		PromotionPlan:    "promotionplan-a",
		Stage:            "prod",
		Version:          "repo@sha256:abc",
		Phase:            kaprov1alpha1.TargetPhaseVerification,
	}

	result, err := r.handleVerification(context.Background(), promotionrun, target, nil)
	if err != nil {
		t.Fatalf("handleVerification returned error: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("expected terminal result, got %+v", result)
	}
	if got := target.Phase; got != kaprov1alpha1.TargetPhaseFailed {
		t.Fatalf("expected target to fail, got %q", got)
	}
}

func TestHandleApplying_RespectsActivePromotionRunClaim(t *testing.T) {
	scheme := controllerTestScheme(t)
	mc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec:       kaprov1alpha1.FleetClusterSpec{Delivery: kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"}},
		Status: kaprov1alpha1.FleetClusterStatus{
			ActivePromotionRun: "other-promotionrun",
			CurrentVersions:    map[string]string{"default": "repo@sha256:old"},
			LastHeartbeat:      time.Now().UTC().Format(time.RFC3339),
		},
	}

	r := &PromotionTargetReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kaprov1alpha1.FleetCluster{}).WithObjects(mc).Build(),
		Recorder: record.NewFakeRecorder(10),
	}
	promotionrun := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"},
	}
	target := &kaprov1alpha1.TargetStatus{
		PromotionRunRef:  "rel-1",
		Target:           "cluster-a",
		PromotionPlanRef: "wave-1",
		PromotionPlan:    "promotionplan-a",
		Stage:            "prod",
		Version:          "repo@sha256:new",
		Phase:            kaprov1alpha1.TargetPhaseApplying,
	}

	result, err := r.handleApplying(context.Background(), promotionrun, target)
	if err != nil {
		t.Fatalf("handleApplying returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while another promotionrun owns the cluster, got %+v", result)
	}
	if target.ApplyIssued {
		t.Fatal("expected ApplyIssued to remain false when cluster is claimed by another promotionrun")
	}
}

func TestHandlePending_PullModeWaitsForFreshHeartbeat(t *testing.T) {
	scheme := controllerTestScheme(t)
	mc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
	}
	r := &PromotionTargetReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc).Build(),
		Recorder: record.NewFakeRecorder(10),
	}
	promotionrun := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1"}}
	target := &kaprov1alpha1.TargetStatus{
		Target: "cluster-a",
		Phase:  kaprov1alpha1.TargetPhasePending,
	}

	result, err := r.handlePending(context.Background(), promotionrun, target)
	if err != nil {
		t.Fatalf("handlePending returned error: %v", err)
	}
	if result.RequeueAfter != requeueNormal {
		t.Fatalf("expected normal requeue for stale heartbeat, got %+v", result)
	}
	if target.Phase != kaprov1alpha1.TargetPhasePending {
		t.Fatalf("expected target to remain Pending, got %q", target.Phase)
	}
	if target.HeartbeatStaleSince == "" {
		t.Fatal("expected HeartbeatStaleSince to be recorded")
	}
}

func TestHandlePending_FreshLeaseHeartbeatAllowsPullTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	now := metav1.NewMicroTime(time.Now().UTC())
	mc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
	}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      heartbeatLeaseName("cluster-a"),
			Namespace: defaultHeartbeatNamespace,
		},
		Spec: coordinationv1.LeaseSpec{RenewTime: &now},
	}
	r := &PromotionTargetReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, lease).Build(),
		Recorder: record.NewFakeRecorder(10),
	}
	promotionrun := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1"}}
	target := &kaprov1alpha1.TargetStatus{
		Target:              "cluster-a",
		Phase:               kaprov1alpha1.TargetPhasePending,
		HeartbeatStaleSince: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}

	result, err := r.handlePending(context.Background(), promotionrun, target)
	if err != nil {
		t.Fatalf("handlePending returned error: %v", err)
	}
	if !result.Requeue || result.RequeueAfter != 0 { //nolint:staticcheck
		t.Fatalf("expected immediate requeue after phase advance, got %+v", result)
	}
	if target.Phase != kaprov1alpha1.TargetPhaseVerification {
		t.Fatalf("expected target to advance to Verification, got %q", target.Phase)
	}
	if target.HeartbeatStaleSince != "" {
		t.Fatalf("expected HeartbeatStaleSince to reset, got %q", target.HeartbeatStaleSince)
	}
	if target.HeartbeatStaleCount != 0 {
		t.Fatalf("expected HeartbeatStaleCount to reset, got %d", target.HeartbeatStaleCount)
	}
}

func TestHandlePending_StaleHeartbeatEventuallyFailsPullTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	mc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
		Status: kaprov1alpha1.FleetClusterStatus{
			LastHeartbeat: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}
	r := &PromotionTargetReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc).Build(),
		Recorder: record.NewFakeRecorder(10),
	}
	promotionrun := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1"}}
	target := &kaprov1alpha1.TargetStatus{
		Target:              "cluster-a",
		Phase:               kaprov1alpha1.TargetPhasePending,
		HeartbeatStaleSince: time.Now().Add(-heartbeatStaleFailAfter - time.Second).UTC().Format(time.RFC3339),
		HeartbeatStaleCount: missingMCFailThreshold - 1,
	}

	result, err := r.handlePending(context.Background(), promotionrun, target)
	if err != nil {
		t.Fatalf("handlePending returned error: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("expected terminal result, got %+v", result)
	}
	if target.Phase != kaprov1alpha1.TargetPhaseFailed {
		t.Fatalf("expected stale heartbeat to fail target, got %q", target.Phase)
	}
	if !strings.Contains(target.Message, "heartbeat stale") {
		t.Fatalf("expected heartbeat failure message, got %q", target.Message)
	}
}

func TestFleetClusterHeartbeat_EmptyLeaseFallsBackToStatusHeartbeat(t *testing.T) {
	scheme := controllerTestScheme(t)
	freshStatus := time.Now().UTC().Format(time.RFC3339)
	mc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status: kaprov1alpha1.FleetClusterStatus{
			LastHeartbeat: freshStatus,
		},
	}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      heartbeatLeaseName("cluster-a"),
			Namespace: defaultHeartbeatNamespace,
		},
	}
	r := &PromotionTargetReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, lease).Build(),
	}

	status, err := r.fleetClusterHeartbeat(context.Background(), mc)
	if err != nil {
		t.Fatalf("fleetClusterHeartbeat returned error: %v", err)
	}
	if !status.Fresh {
		t.Fatalf("expected fresh fallback status heartbeat, got %+v", status)
	}
	if status.Source != "status" {
		t.Fatalf("expected status heartbeat source, got %q", status.Source)
	}
}

func TestBuildApprovalURLs_SingleApproverHintSignedIntoToken(t *testing.T) {
	promotionrun := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rel-1",
			Namespace: "default",
			UID:       "uid-1",
		},
	}
	target := &kaprov1alpha1.TargetStatus{
		Target:           "cluster-a",
		PromotionPlanRef: "wave-1",
		Stage:            "prod",
		Version:          "repo@sha256:abc",
		Gate: &kaprov1alpha1.GatePolicySpec{
			Approval: &kaprov1alpha1.ApprovalConfig{
				Approvers: []string{"alice@example.com"},
			},
		},
	}

	approveURL, _, err := buildApprovalURLs("https://kapro.example.com", []byte("secret"), promotionrun, target)
	if err != nil {
		t.Fatalf("buildApprovalURLs returned error: %v", err)
	}
	tokenStr := approveURL[strings.LastIndex(approveURL, "token=")+len("token="):]
	claims, err := token.Verify(tokenStr, []byte("secret"))
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if claims.ApprovedBy != "alice@example.com" {
		t.Fatalf("expected ApprovedBy claim to be signed, got %q", claims.ApprovedBy)
	}
}

func TestAdvanceTargetUntilStable_CollapsesImmediateTransitions(t *testing.T) {
	scheme := controllerTestScheme(t)
	mc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status: kaprov1alpha1.FleetClusterStatus{
			LastHeartbeat: time.Now().UTC().Format(time.RFC3339),
			Health: kaprov1alpha1.ClusterHealth{
				AllWorkloadsReady: true,
			},
		},
	}
	reg := gatepkg.NewRegistry()
	reg.MustRegister("verification", staticGate{
		result: gatepkg.Result{Phase: kaprov1alpha1.GatePhasePassed},
	})
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc).Build()
	r := &PromotionTargetReconciler{
		Client:       fakeClient,
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	promotionrun := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"},
	}
	target := &kaprov1alpha1.TargetStatus{
		PromotionRunRef:  "rel-1",
		Target:           "cluster-a",
		PromotionPlanRef: "wave-1",
		PromotionPlan:    "promotionplan-a",
		Stage:            "prod",
		Version:          "repo@sha256:abc",
	}

	result, err := r.advanceTargetUntilStable(context.Background(), promotionrun, target, nil)
	if err != nil {
		t.Fatalf("advanceTargetUntilStable returned error: %v", err)
	}
	if !result.Requeue || result.RequeueAfter != 0 { //nolint:staticcheck
		t.Fatalf("expected an immediate requeue after persisting Applying, got %+v", result)
	}
	if target.Phase != kaprov1alpha1.TargetPhaseApplying {
		t.Fatalf("expected collapsed phase to reach Applying, got %q", target.Phase)
	}
}

func TestEvaluateGateTemplates_InconclusiveSkipPasses(t *testing.T) {
	reg := gatepkg.NewRegistry()
	reg.MustRegister("mock", staticGate{
		result: gatepkg.Result{
			Phase:   kaprov1alpha1.GatePhaseInconclusive,
			Message: "uncertain",
		},
	})
	r := &PromotionTargetReconciler{
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	promotionrun := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"}}
	target := &kaprov1alpha1.TargetStatus{Target: "cluster-a", PhaseEnteredAt: time.Now().UTC().Format(time.RFC3339)}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Templates: []kaprov1alpha1.GateTemplateSpec{{
				Name:               "gate-1",
				Type:               "mock",
				InconclusivePolicy: "skip",
			}},
		},
	}

	allPassed, _, err := r.evaluateGateTemplates(context.Background(), promotionrun, target, &gatepkg.Context{}, policy)
	if err != nil {
		t.Fatalf("evaluateGateTemplates returned error: %v", err)
	}
	if !allPassed {
		t.Fatal("expected inconclusivePolicy=skip to allow progress")
	}
	if got := target.Gates[0].Phase; got != kaprov1alpha1.GatePhasePassed {
		t.Fatalf("expected skipped gate to be marked Passed, got %q", got)
	}
}

func TestEvaluateGateTemplates_PersistsEvidence(t *testing.T) {
	reg := gatepkg.NewRegistry()
	reg.MustRegister("mock", staticGate{
		result: gatepkg.Result{
			Phase:   kaprov1alpha1.GatePhasePassed,
			Message: "ok",
			Evidence: []gatepkg.Evidence{{
				Type:          "metric",
				AnalysisMode:  "threshold",
				ObservedValue: "1",
				Threshold:     "0",
				Reason:        "value satisfied threshold",
			}},
		},
	})
	r := &PromotionTargetReconciler{
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	promotionrun := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"}}
	target := &kaprov1alpha1.TargetStatus{Target: "cluster-a", PhaseEnteredAt: time.Now().UTC().Format(time.RFC3339)}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Templates: []kaprov1alpha1.GateTemplateSpec{{
				Name: "gate-1",
				Type: "mock",
			}},
		},
	}

	allPassed, _, err := r.evaluateGateTemplates(context.Background(), promotionrun, target, &gatepkg.Context{}, policy)
	if err != nil {
		t.Fatalf("evaluateGateTemplates returned error: %v", err)
	}
	if !allPassed {
		t.Fatal("expected gate to pass")
	}
	if len(target.Gates) != 1 {
		t.Fatalf("expected one persisted gate, got %d", len(target.Gates))
	}
	if len(target.Gates[0].Evidence) != 1 {
		t.Fatalf("expected one evidence entry, got %d", len(target.Gates[0].Evidence))
	}
	if got := target.Gates[0].Evidence[0].AnalysisMode; got != "threshold" {
		t.Fatalf("expected threshold evidence, got %q", got)
	}
}

func TestGateForTemplate_PluginResolvesPluginName(t *testing.T) {
	reg := gatepkg.NewRegistry()
	pluginGate := staticGate{result: gatepkg.Result{Phase: kaprov1alpha1.GatePhasePassed}}
	reg.MustRegister("slo", pluginGate)

	r := &PromotionTargetReconciler{GateRegistry: reg}
	resolved, err := r.gateForTemplate(&kaprov1alpha1.GateTemplateSpec{
		Type:   "plugin",
		Plugin: &kaprov1alpha1.PluginGateSpec{Name: "slo"},
	})
	if err != nil {
		t.Fatalf("gateForTemplate returned error: %v", err)
	}
	if resolved == nil {
		t.Fatal("expected resolved plugin gate")
	}
}

func TestGateForTemplate_PluginRequiresName(t *testing.T) {
	r := &PromotionTargetReconciler{GateRegistry: gatepkg.NewRegistry()}
	_, err := r.gateForTemplate(&kaprov1alpha1.GateTemplateSpec{Type: "plugin"})
	if err == nil || !strings.Contains(err.Error(), "plugin.name") {
		t.Fatalf("error=%v, want missing plugin.name error", err)
	}
}

func TestEvaluateGateTemplates_FailureRetryStaysRetryableUntilMaxAttempts(t *testing.T) {
	reg := gatepkg.NewRegistry()
	reg.MustRegister("mock", staticGate{
		result: gatepkg.Result{
			Phase:      kaprov1alpha1.GatePhaseFailed,
			Message:    "try again",
			RetryAfter: "12s",
		},
	})
	r := &PromotionTargetReconciler{
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	promotionrun := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"}}
	target := &kaprov1alpha1.TargetStatus{Target: "cluster-a", PhaseEnteredAt: time.Now().UTC().Format(time.RFC3339)}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Templates: []kaprov1alpha1.GateTemplateSpec{{
				Name:          "gate-1",
				Type:          "mock",
				FailurePolicy: "retry",
				MaxAttempts:   3,
			}},
		},
	}

	allPassed, requeueAfter, err := r.evaluateGateTemplates(context.Background(), promotionrun, target, &gatepkg.Context{}, policy)
	if err != nil {
		t.Fatalf("evaluateGateTemplates returned error: %v", err)
	}
	if allPassed {
		t.Fatal("expected failurePolicy=retry to block and retry")
	}
	if requeueAfter != 12*time.Second {
		t.Fatalf("expected retryAfter=12s, got %s", requeueAfter)
	}
	if got := target.Gates[0].Phase; got != kaprov1alpha1.GatePhaseRunning {
		t.Fatalf("expected retrying gate to be Running, got %q", got)
	}

	target.Gates[0].Attempts = 2
	allPassed, _, err = r.evaluateGateTemplates(context.Background(), promotionrun, target, &gatepkg.Context{}, policy)
	if err != nil {
		t.Fatalf("evaluateGateTemplates returned error: %v", err)
	}
	if allPassed {
		t.Fatal("expected exhausted retries to fail the gate")
	}
	if got := target.Gates[0].Phase; got != kaprov1alpha1.GatePhaseFailed {
		t.Fatalf("expected gate to fail after maxAttempts, got %q", got)
	}
}

func TestMetricsGateTimedOut_InvalidTimeoutFailsClosed(t *testing.T) {
	target := &kaprov1alpha1.TargetStatus{PhaseEnteredAt: time.Now().UTC().Format(time.RFC3339)}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{GateTimeout: "not-a-duration"},
	}
	timedOut, msg := metricsGateTimedOut(target, policy)
	if !timedOut {
		t.Fatal("expected invalid gateTimeout to fail closed")
	}
	if !strings.Contains(msg, "invalid gateTimeout") {
		t.Fatalf("expected invalid gateTimeout message, got %q", msg)
	}
}

func TestEventTypeForPhase_AllPhasesReturnNonEmpty(t *testing.T) {
	phases := []kaprov1alpha1.TargetPhase{
		kaprov1alpha1.TargetPhasePending,
		kaprov1alpha1.TargetPhaseVerification,
		kaprov1alpha1.TargetPhaseHealthCheck,
		kaprov1alpha1.TargetPhaseSoaking,
		kaprov1alpha1.TargetPhaseMetricsCheck,
		kaprov1alpha1.TargetPhaseWaitingApproval,
		kaprov1alpha1.TargetPhaseApplying,
		kaprov1alpha1.TargetPhaseConverged,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	}
	for _, phase := range phases {
		typ := eventTypeForPhase(phase)
		if typ == "" {
			t.Errorf("eventTypeForPhase(%q) returned empty", phase)
		}
		if !strings.HasPrefix(typ, "kapro.promotionrun.") {
			t.Errorf("eventTypeForPhase(%q) = %q, want kapro.promotionrun.* prefix", phase, typ)
		}
	}
	// Empty phase should return empty (no notification)
	if got := eventTypeForPhase(""); got != "" {
		t.Errorf("eventTypeForPhase(\"\") = %q, want empty", got)
	}
}
