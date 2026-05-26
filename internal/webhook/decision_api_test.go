package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func decisionTestServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add runtime scheme: %v", err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaproruntimev1alpha1.Target{}, &kaprov1alpha1.Policy{})
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	c := builder.Build()
	return &Server{
		Client:                c,
		DecisionReader:        c,
		OperatorNamespace:     "default",
		DecisionAPIEnabled:    true,
		DecisionAuthenticator: fakeDecisionAuthenticator{user: "system:serviceaccount:kapro-system:test-agent"},
		DecisionAuthorizer:    fakeDecisionAuthorizer{},
	}
}

type fakeDecisionAuthenticator struct {
	user string
}

func (f fakeDecisionAuthenticator) Authenticate(_ context.Context, token string) (*authnv1.UserInfo, error) {
	if token == "" || token == "bad-token" {
		return nil, errors.New("bad token")
	}
	return &authnv1.UserInfo{Username: f.user, Groups: []string{"system:serviceaccounts"}}, nil
}

type fakeDecisionAuthorizer struct {
	deny         bool
	denyResource string
}

func (f fakeDecisionAuthorizer) Authorize(_ context.Context, _ authnv1.UserInfo, attrs authzv1.ResourceAttributes) error {
	if f.deny || (f.denyResource != "" && f.denyResource == attrs.Resource) {
		return errors.New("denied")
	}
	return nil
}

type recordingDecisionAuthorizer struct {
	attrs []authzv1.ResourceAttributes
}

func (r *recordingDecisionAuthorizer) Authorize(_ context.Context, _ authnv1.UserInfo, attrs authzv1.ResourceAttributes) error {
	r.attrs = append(r.attrs, attrs)
	return nil
}

func authorizeDecisionRequest(req *http.Request) {
	req.Header.Set("Authorization", "Bearer test-token-123")
}

func decisionFixtures() (*kaproruntimev1alpha1.PromotionRun, *kaprov1alpha1.Cluster, *kaprov1alpha1.Plan, *kaproruntimev1alpha1.Target) {
	promotionrun := &kaproruntimev1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", UID: "uid-1"},
		Spec: kaprov1alpha1.PromotionRunSpec{
			Version: "registry.example.com/myapp@sha256:v1",
			Plans:   []kaprov1alpha1.PlanRef{{Name: "main", Plan: "std-plan"}},
		},
		Status: kaprov1alpha1.PromotionRunStatus{
			Phase:     kaprov1alpha1.PromotionRunPhaseProgressing,
			StartedAt: "2026-05-09T10:00:00Z",
		},
	}
	mc := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "cluster-a",
			Labels: map[string]string{"tier": "canary", "region": "eu-west"},
		},
		Spec: kaprov1alpha1.ClusterSpec{
			Delivery: kaprov1alpha1.SubstrateBindingSpec{Mode: "pull", Ref: "flux"},
		},
		Status: kaprov1alpha1.ClusterStatus{
			Phase:         kaprov1alpha1.ClusterPhaseConverged,
			LastHeartbeat: "2026-05-09T14:00:00Z",
			Health:        kaprov1alpha1.ClusterHealth{AllWorkloadsReady: true, ReadyWorkloads: 5, TotalWorkloads: 5},
		},
	}
	plan := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "std-plan"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "canary", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}}},
			},
		},
	}
	target := &kaproruntimev1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "rel-1-canary-cluster-a",
			Labels: map[string]string{decisionPromotionRunLabel: "rel-1", decisionPhaseLabel: string(kaprov1alpha1.TargetPhaseWaitingApproval)},
		},
		Spec: kaprov1alpha1.TargetSpec{
			PromotionRunRef: "rel-1",
			Target:          "cluster-a",
			Stage:           "canary",
			Version:         "sha256:abc",
		},
		Status: kaprov1alpha1.TargetStatus{
			TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseWaitingApproval},
		},
	}
	return promotionrun, mc, plan, target
}

// --- Fleet endpoint ---

func TestFleet_ReturnsClusterAndPromotionRunSummary(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet", nil)
	authorizeDecisionRequest(req)
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
	if resp.ActivePromotionRuns != 1 {
		t.Errorf("expected 1 active promotionrun, got %d", resp.ActivePromotionRuns)
	}
	if resp.PendingDecisions != 1 {
		t.Errorf("expected 1 pending decision, got %d", resp.PendingDecisions)
	}
	if resp.Page.Limit != defaultDecisionAPILimit {
		t.Errorf("expected default limit %d, got %d", defaultDecisionAPILimit, resp.Page.Limit)
	}
	if !strings.Contains(rec.Body.String(), `"plan"`) || strings.Contains(rec.Body.String(), `"promotionplan"`) {
		t.Fatalf("fleet JSON should use plan key, got: %s", rec.Body.String())
	}
}

func TestFleet_RejectsInvalidLimit(t *testing.T) {
	s := decisionTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet?limit=0", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestFleet_LimitsAndReportsTruncation(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	clusterB := mc.DeepCopy()
	clusterB.Name = "cluster-b"
	clusterC := mc.DeepCopy()
	clusterC.Name = "cluster-c"
	s := decisionTestServer(t, promotionrun, mc, clusterB, clusterC, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet?limit=2", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp FleetSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Clusters) != 2 {
		t.Fatalf("expected 2 bounded clusters, got %d", len(resp.Clusters))
	}
	if !resp.Page.Truncated {
		t.Fatal("expected page to be marked truncated")
	}
	if resp.Page.Counts["clusters"] != 2 {
		t.Fatalf("expected cluster count 2, got %d", resp.Page.Counts["clusters"])
	}
}

func TestFleet_PhaseFilterScansPastFirstPage(t *testing.T) {
	clusterA := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status:     kaprov1alpha1.ClusterStatus{Phase: kaprov1alpha1.ClusterPhaseFailed},
	}
	clusterB := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-b"},
		Status:     kaprov1alpha1.ClusterStatus{Phase: kaprov1alpha1.ClusterPhaseFailed},
	}
	clusterC := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-c"},
		Status: kaprov1alpha1.ClusterStatus{
			Phase:  kaprov1alpha1.ClusterPhaseConverged,
			Health: kaprov1alpha1.ClusterHealth{AllWorkloadsReady: true},
		},
	}
	s := decisionTestServer(t, clusterA, clusterB, clusterC)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet?limit=1&phase=Converged", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp FleetSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Clusters) != 1 || resp.Clusters[0].Name != "cluster-c" {
		t.Fatalf("expected phase filter to find cluster-c after first page, got %#v", resp.Clusters)
	}
	if resp.Page.Truncated {
		t.Fatal("did not expect truncated response after one matching cluster")
	}
}

func TestFleet_PhaseFilterStopsAtScanCap(t *testing.T) {
	objs := make([]client.Object, 0, decisionAPIScanLimitMultiplier+1)
	for i := 0; i < decisionAPIScanLimitMultiplier+1; i++ {
		objs = append(objs, &kaprov1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cluster-%02d", i)},
			Status:     kaprov1alpha1.ClusterStatus{Phase: kaprov1alpha1.ClusterPhaseFailed},
		})
	}
	s := decisionTestServer(t, objs...)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet?limit=1&phase=Converged", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp FleetSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Clusters) != 0 {
		t.Fatalf("expected no converged clusters, got %#v", resp.Clusters)
	}
	if !resp.Page.Truncated {
		t.Fatal("expected sparse phase scan to stop at scan cap and mark response truncated")
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

func TestDecisionAPI_RequiresBearerToken(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet", nil)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d", rec.Code)
	}
}

func TestDecisionAPI_RequiresRBAC(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, target)
	s.DecisionAuthorizer = fakeDecisionAuthorizer{deny: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleFleet(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when RBAC denies request, got %d", rec.Code)
	}
}

// --- PromotionRun Context endpoint ---

func TestPromotionRunContext_ReturnsPromotionRunAndTargets(t *testing.T) {
	promotionrun, mc, plan, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, plan, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/promotionruns/rel-1/context", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handlePromotionRunContext(rec, req, "rel-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp PromotionRunContext
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.PromotionRun.Name != "rel-1" {
		t.Errorf("expected promotionrun rel-1, got %s", resp.PromotionRun.Name)
	}
	if resp.Plan == nil {
		t.Error("expected plan to be resolved")
	}
	if !strings.Contains(rec.Body.String(), `"plan"`) || strings.Contains(rec.Body.String(), `"promotionplan"`) {
		t.Fatalf("context JSON should use plan key, got: %s", rec.Body.String())
	}
	if len(resp.Targets) != 1 {
		t.Errorf("expected 1 target, got %d", len(resp.Targets))
	}
}

func TestPromotionRunContext_FiltersTargetsWithLimit(t *testing.T) {
	promotionrun, _, plan, target := decisionFixtures()
	completeTarget := target.DeepCopy()
	completeTarget.Name = "rel-1-canary-cluster-b"
	completeTarget.Spec.Target = "cluster-b"
	completeTarget.Status.Phase = kaprov1alpha1.TargetPhaseConverged

	otherRunTarget := target.DeepCopy()
	otherRunTarget.Name = "rel-2-canary-cluster-a"
	otherRunTarget.Spec.PromotionRunRef = "rel-2"

	s := decisionTestServer(t, promotionrun, plan, completeTarget, target, otherRunTarget)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/promotionruns/rel-1/context?limit=1&phase=WaitingApproval", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handlePromotionRunContext(rec, req, "rel-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp PromotionRunContext
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Targets) != 1 {
		t.Fatalf("expected 1 filtered target, got %d", len(resp.Targets))
	}
	if resp.Targets[0].Spec.PromotionRunRef != "rel-1" || resp.Targets[0].Status.Phase != kaprov1alpha1.TargetPhaseWaitingApproval {
		t.Fatalf("unexpected target returned: %#v", resp.Targets[0])
	}
	if resp.Page.Counts["targets"] != 1 {
		t.Fatalf("expected target page count 1, got %d", resp.Page.Counts["targets"])
	}
}

func TestPromotionRunContext_NotFound(t *testing.T) {
	s := decisionTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/promotionruns/nonexistent/context", nil)
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handlePromotionRunContext(rec, req, "nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Gate Context endpoint ---

func TestGateContext_ReturnsTargetAndCluster(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, target)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/promotionruns/rel-1/targets/rel-1-canary-cluster-a/gate", nil)
	authorizeDecisionRequest(req)
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

func TestGateContext_PromotionRunMismatch(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	// Target belongs to rel-1 but we query with rel-1 for a target that
	// claims a different promotionrun. Create a target with mismatched promotionrunRef.
	badTarget := target.DeepCopy()
	badTarget.Name = "bad-target"
	badTarget.Spec.PromotionRunRef = "other-promotionrun"
	s := decisionTestServer(t, promotionrun, badTarget)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	authorizeDecisionRequest(req)
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
	authorizeDecisionRequest(req)
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
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleClusterHealth(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Decide endpoint ---

func postDecision(t *testing.T, s *Server, promotionrunName, targetKey string, req DecisionRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/promotionruns/"+promotionrunName+"/targets/"+targetKey+"/decide", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer test-token-123")
	rec := httptest.NewRecorder()
	s.handleDecide(rec, httpReq, promotionrunName, targetKey)
	return rec
}

func TestDecide_ApproveCreatesApprovalAndTrace(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, target)

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
	var updated kaproruntimev1alpha1.Target
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get updated target: %v", err)
	}
	if updated.Status.DecisionTrace == nil || updated.Status.DecisionTrace.Current == nil {
		t.Fatal("expected DecisionTrace.Current to be set")
	}
	if updated.Status.DecisionTrace.Current.Decision != "Approve" {
		t.Errorf("expected Approve, got %s", updated.Status.DecisionTrace.Current.Decision)
	}
	if updated.Status.DecisionTrace.Current.Identity.Name != "system:serviceaccount:kapro-system:test-agent" {
		t.Errorf("expected authenticated agent identity, got %s", updated.Status.DecisionTrace.Current.Identity.Name)
	}
	if updated.Status.DecisionTrace.Current.Identity.Type != "ServiceAccount" {
		t.Errorf("expected service account identity type, got %s", updated.Status.DecisionTrace.Current.Identity.Type)
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
	if approval.Spec.ApprovedBy != "agent:system:serviceaccount:kapro-system:test-agent" {
		t.Errorf("expected approvedBy authenticated agent, got %s", approval.Spec.ApprovedBy)
	}
}

func TestDecide_ApproveRequiresApprovalCreateRBACBeforeTraceWrite(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, target)
	s.DecisionAuthorizer = fakeDecisionAuthorizer{denyResource: "approvals"}

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.95,
		Reasoning:      "Canary looks healthy.",
		IdempotencyKey: "approval-rbac-denied",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated kaproruntimev1alpha1.Target
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get target: %v", err)
	}
	if updated.Status.DecisionTrace != nil && updated.Status.DecisionTrace.Current != nil {
		t.Fatal("decision trace should not be written when approval create RBAC is denied")
	}
}

func TestDecide_AuthorizesTargetStatusPatchSubresource(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	authz := &recordingDecisionAuthorizer{}
	s := decisionTestServer(t, promotionrun, mc, target)
	s.DecisionAuthorizer = authz

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Reject",
		Confidence:     0.9,
		Reasoning:      "Rejecting for test coverage.",
		IdempotencyKey: "status-patch-rbac",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	for _, attr := range authz.attrs {
		if attr.Group == "kapro.io" &&
			attr.Verb == "patch" &&
			attr.Resource == "targets" &&
			attr.Subresource == "status" &&
			attr.Name == "rel-1-canary-cluster-a" {
			return
		}
	}
	t.Fatalf("missing targets/status patch SAR; attrs=%#v", authz.attrs)
}

func TestDecide_RecordsUserIdentityType(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, target)
	s.DecisionAuthenticator = fakeDecisionAuthenticator{user: "alice@example.com"}

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Reject",
		Confidence:     0.9,
		Reasoning:      "User token decision.",
		IdempotencyKey: "user-identity-type",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated kaproruntimev1alpha1.Target
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get target: %v", err)
	}
	if got := updated.Status.DecisionTrace.Current.Identity.Type; got != "User" {
		t.Fatalf("identity type = %q, want User", got)
	}
}

func TestDecide_RejectDoesNotCreateApproval(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

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
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Defer",
		Confidence:     0.4,
		Reasoning:      "Insufficient data, canary only running 3 minutes.",
		IdempotencyKey: "test-agent-rel-1-cluster-a-defer-1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated kaproruntimev1alpha1.Target
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.DecisionTrace.Current.Decision != "Defer" {
		t.Errorf("expected Defer, got %s", updated.Status.DecisionTrace.Current.Decision)
	}
}

func TestDecide_IdempotentReplay(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

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
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp.Reason != "idempotent replay" {
		t.Errorf("expected idempotent replay, got %s", resp.Reason)
	}
}

func TestDecide_IdempotencyKeyConflict(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

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
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

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
	promotionrun, _, _, target := decisionFixtures()
	target.Status.Phase = kaprov1alpha1.TargetPhaseApplying // not WaitingApproval
	s := decisionTestServer(t, promotionrun, target)

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

func TestDecide_SuspendedPromotionRun(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	promotionrun.Spec.Suspended = true
	s := decisionTestServer(t, promotionrun, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.9,
		Reasoning:      "ok",
		IdempotencyKey: "suspended-1",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for suspended promotionrun, got %d", rec.Code)
	}
}

func TestDecide_InvalidDecisionValue(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

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
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

	rec := postDecision(t, s, "rel-1", "rel-1-canary-cluster-a", DecisionRequest{
		Decision: "Approve",
		// Missing idempotencyKey
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing idempotencyKey, got %d", rec.Code)
	}
}

func TestDecide_PromotionRunNotFound(t *testing.T) {
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

func postOverride(t *testing.T, s *Server, promotionrunName, targetKey string, req OverrideRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/promotionruns/"+promotionrunName+"/targets/"+targetKey+"/override", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer test-token-123")
	rec := httptest.NewRecorder()
	s.handleOverride(rec, httpReq, promotionrunName, targetKey)
	return rec
}

func TestOverride_RecordsHumanOverride(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

	rec := postOverride(t, s, "rel-1", "rel-1-canary-cluster-a", OverrideRequest{
		Action:   "Reject",
		Identity: "sre-oncall",
		Reason:   "Active incident in FI region, holding deployment.",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated kaproruntimev1alpha1.Target
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-canary-cluster-a"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.DecisionTrace == nil || len(updated.Status.DecisionTrace.HumanOverrides) != 1 {
		t.Fatal("expected 1 human override")
	}
	ov := updated.Status.DecisionTrace.HumanOverrides[0]
	if ov.Identity != "system:serviceaccount:kapro-system:test-agent" {
		t.Errorf("expected authenticated override identity, got %s", ov.Identity)
	}
	if ov.Action != "Reject" {
		t.Errorf("expected Reject, got %s", ov.Action)
	}
}

func TestOverride_ApproveCreatesApprovalCR(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

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
	if approval.Spec.ApprovedBy != "system:serviceaccount:kapro-system:test-agent" {
		t.Errorf("expected authenticated approver, got %s", approval.Spec.ApprovedBy)
	}
}

func TestOverride_MissingFields(t *testing.T) {
	promotionrun, _, _, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, target)

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
	promotionrun, mc, plan, target := decisionFixtures()
	s := decisionTestServer(t, promotionrun, mc, plan, target)

	mux := http.NewServeMux()
	s.RegisterDecisionAPI(mux)

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/fleet", http.StatusOK},
		{http.MethodGet, "/api/v1/promotionruns/rel-1/context", http.StatusOK},
		{http.MethodGet, "/api/v1/promotionruns/rel-1/targets/rel-1-canary-cluster-a/gate", http.StatusOK},
		{http.MethodGet, "/api/v1/clusters/cluster-a/health", http.StatusOK},
		{http.MethodGet, "/api/v1/promotionruns/rel-1/targets/rel-1-canary-cluster-a/nonexistent", http.StatusNotFound},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		authorizeDecisionRequest(req)
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
