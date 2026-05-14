package controller

import (
	"context"
	"strings"
	"testing"
	"time"

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

	r := &ReleaseTargetReconciler{
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"},
	}
	target := &kaprov1alpha1.TargetStatus{
		ReleaseRef:  "rel-1",
		Target:      "cluster-a",
		PipelineRef: "wave-1",
		Pipeline:    "pipeline-a",
		Stage:       "prod",
		Version:     "repo@sha256:abc",
		Phase:       kaprov1alpha1.TargetPhaseVerification,
	}

	result, err := r.handleVerification(context.Background(), release, target, nil)
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

func TestHandleApplying_RespectsActiveReleaseClaim(t *testing.T) {
	scheme := controllerTestScheme(t)
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec:       kaprov1alpha1.MemberClusterSpec{Actuator: kaprov1alpha1.ActuatorSpec{Mode: "pull", Backend: "flux"}},
		Status: kaprov1alpha1.MemberClusterStatus{
			ActiveRelease:   "other-release",
			CurrentVersions: map[string]string{"default": "repo@sha256:old"},
		},
	}

	r := &ReleaseTargetReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kaprov1alpha1.MemberCluster{}).WithObjects(mc).Build(),
		Recorder: record.NewFakeRecorder(10),
	}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"},
	}
	target := &kaprov1alpha1.TargetStatus{
		ReleaseRef:  "rel-1",
		Target:      "cluster-a",
		PipelineRef: "wave-1",
		Pipeline:    "pipeline-a",
		Stage:       "prod",
		Version:     "repo@sha256:new",
		Phase:       kaprov1alpha1.TargetPhaseApplying,
	}

	result, err := r.handleApplying(context.Background(), release, target)
	if err != nil {
		t.Fatalf("handleApplying returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while another release owns the cluster, got %+v", result)
	}
	if target.ApplyIssued {
		t.Fatal("expected ApplyIssued to remain false when cluster is claimed by another release")
	}
}

func TestBuildApprovalURLs_SingleApproverHintSignedIntoToken(t *testing.T) {
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rel-1",
			Namespace: "default",
			UID:       "uid-1",
		},
	}
	target := &kaprov1alpha1.TargetStatus{
		Target:      "cluster-a",
		PipelineRef: "wave-1",
		Stage:       "prod",
		Version:     "repo@sha256:abc",
		Gate: &kaprov1alpha1.GatePolicySpec{
			Approval: &kaprov1alpha1.ApprovalConfig{
				Approvers: []string{"alice@example.com"},
			},
		},
	}

	approveURL, _, err := buildApprovalURLs("https://kapro.example.com", []byte("secret"), release, target)
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
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status: kaprov1alpha1.MemberClusterStatus{
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
	r := &ReleaseTargetReconciler{
		Client:       fakeClient,
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"},
	}
	target := &kaprov1alpha1.TargetStatus{
		ReleaseRef:  "rel-1",
		Target:      "cluster-a",
		PipelineRef: "wave-1",
		Pipeline:    "pipeline-a",
		Stage:       "prod",
		Version:     "repo@sha256:abc",
	}

	result, err := r.advanceTargetUntilStable(context.Background(), release, target, nil)
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
	r := &ReleaseTargetReconciler{
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	release := &kaprov1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"}}
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

	allPassed, _, err := r.evaluateGateTemplates(context.Background(), release, target, &gatepkg.Context{}, policy)
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

func TestEvaluateGateTemplates_FailureRetryStaysRetryableUntilMaxAttempts(t *testing.T) {
	reg := gatepkg.NewRegistry()
	reg.MustRegister("mock", staticGate{
		result: gatepkg.Result{
			Phase:      kaprov1alpha1.GatePhaseFailed,
			Message:    "try again",
			RetryAfter: "12s",
		},
	})
	r := &ReleaseTargetReconciler{
		Recorder:     record.NewFakeRecorder(10),
		GateRegistry: reg,
	}
	release := &kaprov1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default"}}
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

	allPassed, requeueAfter, err := r.evaluateGateTemplates(context.Background(), release, target, &gatepkg.Context{}, policy)
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
	allPassed, _, err = r.evaluateGateTemplates(context.Background(), release, target, &gatepkg.Context{}, policy)
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
		if !strings.HasPrefix(typ, "kapro.release.") {
			t.Errorf("eventTypeForPhase(%q) = %q, want kapro.release.* prefix", phase, typ)
		}
	}
	// Empty phase should return empty (no notification)
	if got := eventTypeForPhase(""); got != "" {
		t.Errorf("eventTypeForPhase(\"\") = %q, want empty", got)
	}
}
