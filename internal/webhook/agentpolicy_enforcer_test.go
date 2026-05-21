package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func makeAgentPolicy(name string, saName string, mode kaprov1alpha2.AgentPolicyMode, minConf float64) *kaprov1alpha2.Policy {
	return &kaprov1alpha2.Policy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha2.PolicySpec{
			Identity: kaprov1alpha2.AgentPolicyIdentity{
				ServiceAccountName:      saName,
				ServiceAccountNamespace: "kapro-system",
			},
			Mode: mode,
			Scope: kaprov1alpha2.AgentScope{
				Stages: []string{"canary", "prod-wave-1"},
			},
			Confidence: kaprov1alpha2.AgentConfidencePolicy{
				Default: minConf,
			},
			Escalation: kaprov1alpha2.AgentEscalationPolicy{
				Action: kaprov1alpha2.EscalationHold,
			},
			Audit: kaprov1alpha2.AgentAuditRequirements{
				RequireReasoning:       true,
				RequireConfidenceScore: true,
				MinReasoningLength:     10,
			},
			Priority: 100,
		},
	}
}

func TestEnforce_AllowsValidDecision(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.8)
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if !pd.Allowed {
		t.Fatalf("expected allowed, got denied: %s", pd.DenyReason)
	}
	if pd.EffectiveMode != kaprov1alpha2.AgentPolicyModeAuto {
		t.Errorf("expected auto mode, got %s", pd.EffectiveMode)
	}
}

func TestEnforce_DeniesLowConfidence(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.9)
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.7, 50)
	if pd.Allowed {
		t.Fatal("expected denied for low confidence")
	}
	if pd.DenyReason == "" {
		t.Error("expected deny reason")
	}
}

func TestEnforce_DeniesExcludedStage(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.Scope.ExcludeStages = []string{"canary"}
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if pd.Allowed {
		t.Fatal("expected denied for excluded stage")
	}
}

func TestEnforce_DeniesStageNotInScope(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.Scope.Stages = []string{"prod-wave-1"} // canary not in list
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if pd.Allowed {
		t.Fatal("expected denied for stage not in scope")
	}
}

func TestEnforce_DeniesExcludedCluster(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.Scope.ExcludeClusters = []string{"cluster-a"}
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if pd.Allowed {
		t.Fatal("expected denied for excluded cluster")
	}
}

func TestEnforce_DeniesSuspendedPolicy(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.Suspended = true
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if pd.Allowed {
		t.Fatal("expected denied for suspended policy")
	}
}

func TestEnforce_DeniesDisabledMode(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeDisabled, 0.5)
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if pd.Allowed {
		t.Fatal("expected denied for disabled mode")
	}
}

func TestEnforce_DeniesShortReasoning(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.Audit.MinReasoningLength = 100
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 20) // too short
	if pd.Allowed {
		t.Fatal("expected denied for short reasoning")
	}
}

func TestEnforce_NoPolicyDenies(t *testing.T) {
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(nil, target, mc, 0.95, 50)
	if pd.Allowed {
		t.Fatal("expected denied when no policy exists")
	}
}

func TestEnforce_RecommendModeChangesEffective(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeRecommend, 0.5)
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if !pd.Allowed {
		t.Fatalf("expected allowed, got: %s", pd.DenyReason)
	}
	if pd.EffectiveMode != kaprov1alpha2.AgentPolicyModeRecommend {
		t.Errorf("expected recommend mode, got %s", pd.EffectiveMode)
	}
}

func TestEnforce_CountryProfileTightensConfidence(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.8)
	policy.Spec.Scope.CountryProfiles = []kaprov1alpha2.CountryRiskProfile{
		{
			Countries:     []string{"eu-west"},
			RiskTier:      "high",
			MinConfidence: 0.99, // tighter than default 0.8
		},
	}
	_, mc, _, target := decisionFixtures()
	// mc has label region=eu-west but we check country label
	mc.Labels["country"] = "eu-west"
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50) // below 0.99
	if pd.Allowed {
		t.Fatal("expected denied — country profile tightens confidence to 0.99")
	}
}

func TestEnforce_CountryProfileRequiresHumanCosign(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.Scope.CountryProfiles = []kaprov1alpha2.CountryRiskProfile{
		{
			Countries:          []string{"fi"},
			RiskTier:           "critical",
			MinConfidence:      0.5,
			RequireHumanCosign: true,
		},
	}
	_, mc, _, target := decisionFixtures()
	mc.Labels["country"] = "fi"
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if !pd.Allowed {
		t.Fatalf("expected allowed, got: %s", pd.DenyReason)
	}
	if !pd.RequireHumanCosign {
		t.Error("expected RequireHumanCosign=true for fi")
	}
}

func TestEnforce_TierOverrideTightensConfidence(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.7)
	policy.Spec.Confidence.TierOverrides = map[string]float64{"canary": 0.95}
	_, mc, _, target := decisionFixtures()
	pd := enforceAgentPolicy(policy, target, mc, 0.9, 50) // below 0.95 tier override
	if pd.Allowed {
		t.Fatal("expected denied — tier override tightens to 0.95")
	}
}

func TestReserve_RateLimitDeniesAtMax(t *testing.T) {
	// Rate limits moved from enforceAgentPolicy to reserveAgentPolicySlot in
	// gate-B2 so the check + counter increment happen in one CAS pass.
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.RateLimits = &kaprov1alpha2.AgentRateLimits{MaxApprovalsPerDay: 5}
	policy.Status.DecisionsToday = 5
	s := decisionTestServer(t, policy)
	ok, reason, err := s.reserveAgentPolicySlot(httpReq(t).Context(), policy.DeepCopy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected denied — daily limit reached")
	}
	if reason == "" {
		t.Fatal("expected deny reason")
	}
}

func TestReserve_ConcurrentLimitDenies(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.RateLimits = &kaprov1alpha2.AgentRateLimits{MaxConcurrent: 3}
	policy.Status.ActiveDecisions = 3
	s := decisionTestServer(t, policy)
	ok, reason, err := s.reserveAgentPolicySlot(httpReq(t).Context(), policy.DeepCopy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected denied — concurrent limit reached")
	}
	if reason == "" {
		t.Fatal("expected deny reason")
	}
}

func TestReserve_IncrementsCountersOnSuccess(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.RateLimits = &kaprov1alpha2.AgentRateLimits{MaxApprovalsPerDay: 5}
	s := decisionTestServer(t, policy)
	local := policy.DeepCopy()
	ok, _, err := s.reserveAgentPolicySlot(httpReq(t).Context(), local)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected slot allowed under limit")
	}
	// Confirm the increment landed in etcd, not just on the local copy.
	var fresh kaprov1alpha2.Policy
	if err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: policy.Name}, &fresh); err != nil {
		t.Fatalf("get refreshed policy: %v", err)
	}
	if fresh.Status.DecisionsToday != 1 {
		t.Fatalf("DecisionsToday = %d, want 1", fresh.Status.DecisionsToday)
	}
	if fresh.Status.ActiveDecisions != 1 {
		t.Fatalf("ActiveDecisions = %d, want 1", fresh.Status.ActiveDecisions)
	}
}

func TestEnforce_ClusterSelectorMismatch(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.Scope.ClusterSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"tier": "prod"},
	}
	_, mc, _, target := decisionFixtures()
	// mc has tier=canary, selector wants tier=prod
	pd := enforceAgentPolicy(policy, target, mc, 0.95, 50)
	if pd.Allowed {
		t.Fatal("expected denied — cluster labels don't match selector")
	}
}

// --- Integration: Decision API with AgentPolicy ---

func TestDecide_WithAgentPolicy_Denied(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	policy := makeAgentPolicy("strict-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.99)
	s := decisionTestServer(t, promotionrun, mc, target, policy)

	body, _ := json.Marshal(DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.8, // below 0.99 threshold
		Reasoning:      "Looks ok but not fully confident about this deployment.",
		IdempotencyKey: "policy-test-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleDecide(rec, req, "rel-1", "rel-1-canary-cluster-a")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for policy violation, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDecide_WithAgentPolicy_Allowed(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	policy := makeAgentPolicy("loose-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.7)
	s := decisionTestServer(t, promotionrun, mc, target, policy)

	body, _ := json.Marshal(DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.95,
		Reasoning:      "Error rate well below threshold, canary healthy for 30 minutes.",
		IdempotencyKey: "policy-test-2",
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleDecide(rec, req, "rel-1", "rel-1-canary-cluster-a")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDecide_RecommendModeDoesNotCreateApproval(t *testing.T) {
	promotionrun, mc, _, target := decisionFixtures()
	policy := makeAgentPolicy("recommend-policy", "test-agent", kaprov1alpha2.AgentPolicyModeRecommend, 0.5)
	s := decisionTestServer(t, promotionrun, mc, target, policy)

	body, _ := json.Marshal(DecisionRequest{
		Decision:       "Approve",
		Confidence:     0.95,
		Reasoning:      "Recommended approval based on healthy canary metrics.",
		IdempotencyKey: "recommend-test-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	authorizeDecisionRequest(req)
	rec := httptest.NewRecorder()
	s.handleDecide(rec, req, "rel-1", "rel-1-canary-cluster-a")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp DecisionResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.EffectiveDecision != "Recommended" {
		t.Errorf("expected Recommended, got %s", resp.EffectiveDecision)
	}

	// Approval should NOT be created in recommend mode.
	var approval kaprov1alpha2.Approval
	err := s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: "rel-1-rel-1-canary-cluster-a"}, &approval)
	if err == nil {
		t.Error("expected no Approval CR in recommend mode")
	}
}

func TestReserve_ResetsDecisionsTodayAcrossUTCDay(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Spec.RateLimits = &kaprov1alpha2.AgentRateLimits{MaxApprovalsPerDay: 5}
	// Simulate yesterday's decision count at the limit, with LastDecisionAt
	// from a prior UTC day. The reservation should reset DecisionsToday
	// to 0 BEFORE checking the limit, so it's allowed.
	yesterday := time.Now().UTC().AddDate(0, 0, -2).Format(time.RFC3339)
	policy.Status.DecisionsToday = 5
	policy.Status.LastDecisionAt = yesterday

	s := decisionTestServer(t, policy)
	local := policy.DeepCopy()
	ok, reason, err := s.reserveAgentPolicySlot(httpReq(t).Context(), local)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected allowed after day rollover, denied: %s", reason)
	}

	var fresh kaprov1alpha2.Policy
	_ = s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: policy.Name}, &fresh)
	if fresh.Status.DecisionsToday != 1 {
		t.Fatalf("DecisionsToday = %d, want 1 (was reset to 0 then incremented)", fresh.Status.DecisionsToday)
	}
}

func TestRelease_DecrementsActiveDecisions(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Status.ActiveDecisions = 3
	s := decisionTestServer(t, policy)
	if err := s.releaseAgentPolicySlot(httpReq(t).Context(), policy); err != nil {
		t.Fatalf("release: %v", err)
	}
	var fresh kaprov1alpha2.Policy
	_ = s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: policy.Name}, &fresh)
	if fresh.Status.ActiveDecisions != 2 {
		t.Fatalf("ActiveDecisions = %d, want 2", fresh.Status.ActiveDecisions)
	}
}

func TestRelease_FloorsAtZero(t *testing.T) {
	policy := makeAgentPolicy("test-policy", "test-agent", kaprov1alpha2.AgentPolicyModeAuto, 0.5)
	policy.Status.ActiveDecisions = 0
	s := decisionTestServer(t, policy)
	if err := s.releaseAgentPolicySlot(httpReq(t).Context(), policy); err != nil {
		t.Fatalf("release: %v", err)
	}
	var fresh kaprov1alpha2.Policy
	_ = s.Client.Get(httpReq(t).Context(), client.ObjectKey{Name: policy.Name}, &fresh)
	if fresh.Status.ActiveDecisions != 0 {
		t.Fatalf("ActiveDecisions = %d, want 0 (floored)", fresh.Status.ActiveDecisions)
	}
}
