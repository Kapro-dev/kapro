package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func decisionTestServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.ReleaseTarget{})
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	return &Server{
		Client:            builder.Build(),
		OperatorNamespace: "default",
	}
}

func decisionFixtures() (*kaprov1alpha1.Release, *kaprov1alpha1.MemberCluster, *kaprov1alpha1.Pipeline, *kaprov1alpha1.ReleaseTarget) {
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", UID: "uid-1"},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:  "myapp-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{{Name: "main", Pipeline: "std-pipeline"}},
		},
		Status: kaprov1alpha1.ReleaseStatus{
			Phase:     kaprov1alpha1.ReleasePhaseProgressing,
			StartedAt: "2026-05-09T10:00:00Z",
		},
	}
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "cluster-a",
			Labels: map[string]string{"tier": "canary", "region": "eu-west"},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
		},
		Status: kaprov1alpha1.MemberClusterStatus{
			Phase:         kaprov1alpha1.ClusterPhaseConverged,
			LastHeartbeat: "2026-05-09T14:00:00Z",
			Health:        kaprov1alpha1.ClusterHealth{AllWorkloadsReady: true, ReadyWorkloads: 5, TotalWorkloads: 5},
		},
	}
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "std-pipeline"},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "canary", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}}},
			},
		},
	}
	target := &kaprov1alpha1.ReleaseTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-canary-cluster-a"},
		Spec: kaprov1alpha1.ReleaseTargetSpec{
			ReleaseRef: "rel-1",
			Target:     "cluster-a",
			Stage:      "canary",
			Version:    "sha256:abc",
		},
		Status: kaprov1alpha1.ReleaseTargetStatus{
			TargetStatus: kaprov1alpha1.TargetStatus{Phase: kaprov1alpha1.TargetPhaseWaitingApproval},
		},
	}
	return release, mc, pipeline, target
}

// --- Fleet endpoint ---

func TestFleet_ReturnsClusterAndReleaseSummary(t *testing.T) {
	release, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, release, mc, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet", nil)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp FleetSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalClusters != 1 {
		t.Errorf("expected 1 cluster, got %d", resp.TotalClusters)
	}
	if resp.HealthyClusters != 1 {
		t.Errorf("expected 1 healthy, got %d", resp.HealthyClusters)
	}
	if resp.ActiveReleases != 1 {
		t.Errorf("expected 1 active release, got %d", resp.ActiveReleases)
	}
	if resp.PendingDecisions != 1 {
		t.Errorf("expected 1 pending decision, got %d", resp.PendingDecisions)
	}
}

func TestFleet_RejectsPost(t *testing.T) {
	s := decisionTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet", nil)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// --- Release Context endpoint ---

func TestReleaseContext_ReturnsReleaseAndTargets(t *testing.T) {
	release, mc, pipeline, target := decisionFixtures()
	s := decisionTestServer(t, release, mc, pipeline, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/releases/rel-1/context", nil)
	rec := httptest.NewRecorder()
	s.handleReleaseContext(rec, req, "rel-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp ReleaseContext
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Release.Name != "rel-1" {
		t.Errorf("expected release rel-1, got %s", resp.Release.Name)
	}
	if resp.Pipeline == nil {
		t.Error("expected pipeline to be resolved")
	}
	if len(resp.Targets) != 1 {
		t.Errorf("expected 1 target, got %d", len(resp.Targets))
	}
}

func TestReleaseContext_NotFound(t *testing.T) {
	s := decisionTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/releases/nonexistent/context", nil)
	rec := httptest.NewRecorder()
	s.handleReleaseContext(rec, req, "nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Gate Context endpoint ---

func TestGateContext_ReturnsTargetAndCluster(t *testing.T) {
	release, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, release, mc, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/releases/rel-1/targets/rel-1-canary-cluster-a/gate", nil)
	rec := httptest.NewRecorder()
	s.handleGateContext(rec, req, "rel-1", "rel-1-canary-cluster-a")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp GateContext
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Target.Name != "rel-1-canary-cluster-a" {
		t.Errorf("expected target name, got %s", resp.Target.Name)
	}
	if resp.Cluster == nil {
		t.Error("expected cluster health to be populated")
	}
	if resp.Cluster != nil && !resp.Cluster.Status.Health.AllWorkloadsReady {
		t.Error("expected cluster to be healthy")
	}
}

func TestGateContext_ReleaseMismatch(t *testing.T) {
	release, _, _, target := decisionFixtures()
	// Target belongs to rel-1 but we query with rel-1 for a target that
	// claims a different release. Create a target with mismatched releaseRef.
	badTarget := target.DeepCopy()
	badTarget.Name = "bad-target"
	badTarget.Spec.ReleaseRef = "other-release"
	s := decisionTestServer(t, release, badTarget)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.handleGateContext(rec, req, "rel-1", "bad-target")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on mismatch, got %d", rec.Code)
	}
}

// --- Cluster Health endpoint ---

func TestClusterHealth_ReturnsHealth(t *testing.T) {
	_, mc, _, _ := decisionFixtures()
	s := decisionTestServer(t, mc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters/cluster-a/health", nil)
	rec := httptest.NewRecorder()
	s.handleClusterHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["name"] != "cluster-a" {
		t.Errorf("expected cluster-a, got %v", resp["name"])
	}
}

func TestClusterHealth_NotFound(t *testing.T) {
	s := decisionTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters/nonexistent/health", nil)
	rec := httptest.NewRecorder()
	s.handleClusterHealth(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Decide endpoint ---

func postDecision(t *testing.T, s *Server, releaseName, targetKey string, req DecisionRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/releases/"+releaseName+"/targets/"+targetKey+"/decide", bytes.NewReader(body))
	httpReq.Header.Set("X-Agent-Name", "test-agent")
	httpReq.Header.Set("Authorization", "Bearer test-token-123")
	rec := httptest.NewRecorder()
	s.handleDecide(rec, httpReq, releaseName, targetKey)
	return rec
}

func TestDecide_ApproveCreatesApprovalAndTrace(t *testing.T) {
	release, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, release, mc, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.95,
		Reasoning:      "Canary looks healthy. Error rate 0.1% well below 1% threshold.",
		IdempotencyKey: "test-agent-rel-1-cluster-a-1",
		Factors: []kaprov1alpha1.DecisionFactor{
			{Name: "error_rate", Value: 0.001, Weight: 0.5, Assessment: "pass"},
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp DecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Accepted {
		t.Error("expected accepted=true")
	}
	if resp.DecisionID != "test-agent-rel-1-cluster-a-1" {
		t.Errorf("expected decisionId to match idempotencyKey, got %s", resp.DecisionID)
	}

	// Verify DecisionTrace was written to target status.
	var updated kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get updated target: %v", err)
	}
	if updated.Status.DecisionTrace == nil || updated.Status.DecisionTrace.Current == nil {
		t.Fatal("expected DecisionTrace.Current to be set")
	}
	if updated.Status.DecisionTrace.Current.Decision != "Approve" {
		t.Errorf("expected Approve, got %s", updated.Status.DecisionTrace.Current.Decision)
	}
	if updated.Status.DecisionTrace.Current.Identity.Name != "test-agent" {
		t.Errorf("expected agent name test-agent, got %s", updated.Status.DecisionTrace.Current.Identity.Name)
	}
	if updated.Status.DecisionTrace.Current.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", updated.Status.DecisionTrace.Current.Confidence)
	}
	if len(updated.Status.DecisionTrace.Current.Factors) != 1 {
		t.Errorf("expected 1 factor, got %d", len(updated.Status.DecisionTrace.Current.Factors))
	}

	// Verify Approval CR was created.
	var approval kaprov1alpha1.Approval
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-rel-1-canary-cluster-a"}, &approval); err != nil {
		t.Fatalf("expected Approval to be created: %v", err)
	}
	if approval.Spec.ApprovedBy != "agent:test-agent" {
		t.Errorf("expected approvedBy agent:test-agent, got %s", approval.Spec.ApprovedBy)
	}
}

func TestDecide_RejectDoesNotCreateApproval(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Reject",
		Confidence:     0.88,
		Reasoning:      "Error rate 2.5% exceeds threshold.",
		IdempotencyKey: "test-agent-rel-1-cluster-a-reject-1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no Approval was created.
	var approval kaprov1alpha1.Approval
	err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-rel-1-canary-cluster-a"}, &approval)
	if err == nil {
		t.Error("expected no Approval CR for Reject decision")
	}
}

func TestDecide_DeferRecordsWithoutApproval(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Defer",
		Confidence:     0.4,
		Reasoning:      "Insufficient data, canary only running 3 minutes.",
		IdempotencyKey: "test-agent-rel-1-cluster-a-defer-1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.DecisionTrace.Current.Decision != "Defer" {
		t.Errorf("expected Defer, got %s", updated.Status.DecisionTrace.Current.Decision)
	}
}

func TestDecide_IdempotentReplay(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	req := DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.95,
		Reasoning:      "Looks good.",
		IdempotencyKey: "idem-key-1",
	}

	// First call.
	rec1 := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", req)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d", rec1.Code)
	}

	// Replay with same key and decision.
	rec2 := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp DecisionResponse
	json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp.Reason != "idempotent replay" {
		t.Errorf("expected idempotent replay, got %s", resp.Reason)
	}
}

func TestDecide_IdempotencyKeyConflict(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	// First call: Approve.
	postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.95,
		Reasoning:      "ok",
		IdempotencyKey: "idem-key-2",
	})

	// Second call: same key, different decision.
	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Reject",
		Confidence:     0.5,
		Reasoning:      "changed mind",
		IdempotencyKey: "idem-key-2",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for same key different decision, got %d", rec.Code)
	}
}

func TestDecide_FirstDecisionWins(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	// Agent A approves.
	postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.95,
		Reasoning:      "agent A approves",
		IdempotencyKey: "agent-a-key",
	})

	// Agent B tries to reject — conflict.
	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Reject",
		Confidence:     0.88,
		Reasoning:      "agent B rejects",
		IdempotencyKey: "agent-b-key",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for second agent, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDecide_WrongPhase(t *testing.T) {
	release, _, _, target := decisionFixtures()
	target.Status.Phase = kaprov1alpha1.TargetPhaseApplying // not WaitingApproval
	s := decisionTestServer(t, release, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.9,
		Reasoning:      "ok",
		IdempotencyKey: "wrong-phase-1",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for wrong phase, got %d", rec.Code)
	}
}

func TestDecide_SuspendedRelease(t *testing.T) {
	release, _, _, target := decisionFixtures()
	release.Spec.Suspended = true
	s := decisionTestServer(t, release, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.9,
		Reasoning:      "ok",
		IdempotencyKey: "suspended-1",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for suspended release, got %d", rec.Code)
	}
}

func TestDecide_InvalidDecisionValue(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Maybe",
		Confidence:     0.5,
		Reasoning:      "unsure",
		IdempotencyKey: "invalid-1",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid decision, got %d", rec.Code)
	}
}

func TestDecide_MissingFields(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision: "Approve",
		// Missing idempotencyKey
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing idempotencyKey, got %d", rec.Code)
	}
}

func TestDecide_ReleaseNotFound(t *testing.T) {
	_, _, _, target := decisionFixtures()
	s := decisionTestServer(t, target)

	rec := postDecision(t, s, "nonexistent", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.9,
		Reasoning:      "ok",
		IdempotencyKey: "nf-1",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Override endpoint ---

func postOverride(t *testing.T, s *Server, releaseName, targetKey string, req OverrideRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/releases/"+releaseName+"/targets/"+targetKey+"/override", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleOverride(rec, httpReq, releaseName, targetKey)
	return rec
}

func TestOverride_RecordsHumanOverride(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	rec := postOverride(t, s, "rel-1", "rel-1-canary-cluster-a", OverrideRequest{
		Action:   "Reject",
		Identity: "sre-oncall",
		Reason:   "Active incident in FI region, holding deployment.",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.DecisionTrace == nil || len(updated.Status.DecisionTrace.HumanOverrides) != 1 {
		t.Fatal("expected 1 human override")
	}
	ov := updated.Status.DecisionTrace.HumanOverrides[0]
	if ov.Identity != "sre-oncall" {
		t.Errorf("expected sre-oncall, got %s", ov.Identity)
	}
	if ov.Action != "Reject" {
		t.Errorf("expected Reject, got %s", ov.Action)
	}
}

func TestOverride_ApproveCreatesApprovalCR(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	rec := postOverride(t, s, "rel-1", "rel-1-canary-cluster-a", OverrideRequest{
		Action:   "Approve",
		Identity: "sre-oncall",
		Reason:   "Manual approval after review.",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var approval kaprov1alpha1.Approval
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-rel-1-canary-cluster-a"}, &approval); err != nil {
		t.Fatalf("expected Approval to be created: %v", err)
	}
	if approval.Spec.ApprovedBy != "sre-oncall" {
		t.Errorf("expected sre-oncall, got %s", approval.Spec.ApprovedBy)
	}
}

func TestOverride_MissingFields(t *testing.T) {
	release, _, _, target := decisionFixtures()
	s := decisionTestServer(t, release, target)

	rec := postOverride(t, s, "rel-1", "rel-1-canary-cluster-a", OverrideRequest{
		Action: "Approve",
		// Missing identity and reason
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// --- Router ---

func TestRouter_DispatchesCorrectly(t *testing.T) {
	release, mc, pipeline, target := decisionFixtures()
	s := decisionTestServer(t, release, mc, pipeline, target)

	mux := http.NewServeMux()
	s.RegisterDecisionAPI(mux)

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/fleet", http.StatusOK},
		{http.MethodGet, "/api/v1/releases/rel-1/context", http.StatusOK},
		{http.MethodGet, "/api/v1/releases/rel-1/targets/rel-1-canary-cluster-a/gate", http.StatusOK},
		{http.MethodGet, "/api/v1/clusters/cluster-a/health", http.StatusOK},
		{http.MethodGet, "/api/v1/releases/rel-1/targets/rel-1-canary-cluster-a/nonexistent", http.StatusNotFound},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s %s: expected %d, got %d", tt.method, tt.path, tt.want, rec.Code)
		}
	}
}

// httpReq returns a background context for client operations in tests.
func httpReq(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, "/", nil)
}
