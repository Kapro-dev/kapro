// Package webhook — Decision API endpoints for AI-native progressive delivery.
//
// The Decision API extends the existing webhook server with endpoints that
// allow AI agents to query fleet context and submit deployment decisions.
// All endpoints are mounted under /api/v1/ and authenticated via ServiceAccount JWT.
//
// Context endpoints (read-only):
//
//	GET /api/v1/fleet                                    — fleet-wide summary
//	GET /api/v1/releases/{name}/context                  — full release context
//	GET /api/v1/releases/{name}/targets/{key}/gate       — gate evaluation context
//	GET /api/v1/clusters/{name}/health                   — cluster health
//
// Decision endpoints (mutating):
//
//	POST /api/v1/releases/{name}/targets/{key}/decide    — submit a decision
//	POST /api/v1/releases/{name}/targets/{key}/override  — human override
package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const maxDecisionTraceHistory = 10

// RegisterDecisionAPI mounts the Decision API endpoints on the given mux.
func (s *Server) RegisterDecisionAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/fleet", s.handleFleet)
	mux.HandleFunc("/api/v1/releases/", s.routeRelease)
	mux.HandleFunc("/api/v1/clusters/", s.handleClusterHealth)
}

// --- Fleet Context ---

// FleetSummary is the response for GET /api/v1/fleet.
type FleetSummary struct {
	GeneratedAt      string           `json:"generatedAt"`
	TotalClusters    int              `json:"totalClusters"`
	HealthyClusters  int              `json:"healthyClusters"`
	DegradedClusters int              `json:"degradedClusters"`
	ActiveReleases   int              `json:"activeReleases"`
	PendingDecisions int              `json:"pendingDecisions"`
	Clusters         []ClusterSummary `json:"clusters"`
	Releases         []ReleaseSummary `json:"releases"`
}

// ClusterSummary is a compact view of one MemberCluster.
type ClusterSummary struct {
	Name          string            `json:"name"`
	Labels        map[string]string `json:"labels"`
	Phase         string            `json:"phase"`
	Healthy       bool              `json:"healthy"`
	LastHeartbeat string            `json:"lastHeartbeat,omitempty"`
	Versions      map[string]string `json:"versions,omitempty"`
}

// ReleaseSummary is a compact view of one Release.
type ReleaseSummary struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Pipeline  string `json:"pipeline,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var clusters kaprov1alpha1.MemberClusterList
	if err := s.Client.List(ctx, &clusters); err != nil {
		http.Error(w, "failed to list clusters", http.StatusInternalServerError)
		return
	}

	var releases kaprov1alpha1.ReleaseList
	if err := s.Client.List(ctx, &releases); err != nil {
		http.Error(w, "failed to list releases", http.StatusInternalServerError)
		return
	}

	// Count pending decisions across all active releases.
	var targets kaprov1alpha1.ReleaseTargetList
	if err := s.Client.List(ctx, &targets); err != nil {
		http.Error(w, "failed to list targets", http.StatusInternalServerError)
		return
	}

	healthy, degraded, pendingDecisions := 0, 0, 0
	for _, mc := range clusters.Items {
		if mc.Status.Health.AllWorkloadsReady {
			healthy++
		} else {
			degraded++
		}
	}
	for _, t := range targets.Items {
		if t.Status.Phase == kaprov1alpha1.TargetPhaseWaitingApproval {
			pendingDecisions++
		}
	}

	clusterSummaries := make([]ClusterSummary, 0, len(clusters.Items))
	for _, mc := range clusters.Items {
		clusterSummaries = append(clusterSummaries, ClusterSummary{
			Name:          mc.Name,
			Labels:        mc.Labels,
			Phase:         string(mc.Status.Phase),
			Healthy:       mc.Status.Health.AllWorkloadsReady,
			LastHeartbeat: mc.Status.LastHeartbeat,
			Versions:      mc.Status.CurrentVersions,
		})
	}

	activeReleases := 0
	releaseSummaries := make([]ReleaseSummary, 0, len(releases.Items))
	for _, rel := range releases.Items {
		if rel.Status.Phase == kaprov1alpha1.ReleasePhaseProgressing {
			activeReleases++
		}
		pipeline := ""
		if len(rel.Spec.Pipelines) > 0 {
			pipeline = rel.Spec.Pipelines[0].Pipeline
		}
		releaseSummaries = append(releaseSummaries, ReleaseSummary{
			Name:      rel.Name,
			Phase:     string(rel.Status.Phase),
			Pipeline:  pipeline,
			StartedAt: rel.Status.StartedAt,
		})
	}

	writeJSON(w, http.StatusOK, FleetSummary{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		TotalClusters:    len(clusters.Items),
		HealthyClusters:  healthy,
		DegradedClusters: degraded,
		ActiveReleases:   activeReleases,
		PendingDecisions: pendingDecisions,
		Clusters:         clusterSummaries,
		Releases:         releaseSummaries,
	})
}

// --- Release Context ---

// ReleaseContext is the response for GET /api/v1/releases/{name}/context.
type ReleaseContext struct {
	GeneratedAt string                        `json:"generatedAt"`
	Release     kaprov1alpha1.Release         `json:"release"`
	Pipeline    *kaprov1alpha1.Pipeline       `json:"pipeline,omitempty"`
	Targets     []kaprov1alpha1.ReleaseTarget `json:"targets"`
}

// --- Gate Context ---

// GateContext is the response for GET /api/v1/releases/{name}/targets/{key}/gate.
type GateContext struct {
	GeneratedAt string                       `json:"generatedAt"`
	Target      kaprov1alpha1.ReleaseTarget  `json:"target"`
	Release     kaprov1alpha1.Release        `json:"release"`
	Cluster     *kaprov1alpha1.MemberCluster `json:"cluster,omitempty"`
	Precedents  []DecisionPrecedent          `json:"precedents,omitempty"`
}

// DecisionPrecedent is a historical decision on this target for agent learning.
type DecisionPrecedent struct {
	Release    string  `json:"release"`
	Decision   string  `json:"decision"`
	Confidence float64 `json:"confidence"`
	Outcome    string  `json:"outcome"`
	Agent      string  `json:"agent"`
}

// --- Decision Request/Response ---

// DecisionRequest is the payload for POST /api/v1/releases/{name}/targets/{key}/decide.
type DecisionRequest struct {
	Decision       string                            `json:"decision"`
	Confidence     float64                           `json:"confidence"`
	Reasoning      string                            `json:"reasoning"`
	Factors        []kaprov1alpha1.DecisionFactor    `json:"factors,omitempty"`
	Conditions     []kaprov1alpha1.DecisionCondition `json:"conditions,omitempty"`
	IdempotencyKey string                            `json:"idempotencyKey"`
	ExpiresAt      string                            `json:"expiresAt,omitempty"`
}

// DecisionResponse is the response for POST /decide.
type DecisionResponse struct {
	Accepted          bool   `json:"accepted"`
	DecisionID        string `json:"decisionId,omitempty"`
	EffectiveDecision string `json:"effectiveDecision,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// OverrideRequest is the payload for POST /override.
type OverrideRequest struct {
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Identity string `json:"identity"`
}

// --- Router ---

func (s *Server) routeRelease(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/releases/{name}/context
	//        /api/v1/releases/{name}/targets/{key}/gate
	//        /api/v1/releases/{name}/targets/{key}/decide
	//        /api/v1/releases/{name}/targets/{key}/override
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/releases/")
	parts := strings.Split(path, "/")

	switch {
	case len(parts) == 2 && parts[1] == "context":
		s.handleReleaseContext(w, r, parts[0])
	case len(parts) == 4 && parts[1] == "targets" && parts[3] == "gate":
		s.handleGateContext(w, r, parts[0], parts[2])
	case len(parts) == 4 && parts[1] == "targets" && parts[3] == "decide":
		s.handleDecide(w, r, parts[0], parts[2])
	case len(parts) == 4 && parts[1] == "targets" && parts[3] == "override":
		s.handleOverride(w, r, parts[0], parts[2])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// --- Context Handlers ---

func (s *Server) handleReleaseContext(w http.ResponseWriter, r *http.Request, releaseName string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var release kaprov1alpha1.Release
	if err := s.Client.Get(ctx, client.ObjectKey{Name: releaseName}, &release); err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return
	}

	// Resolve the first pipeline for context.
	var pipeline *kaprov1alpha1.Pipeline
	if len(release.Spec.Pipelines) > 0 {
		var pl kaprov1alpha1.Pipeline
		if err := s.Client.Get(ctx, client.ObjectKey{Name: release.Spec.Pipelines[0].Pipeline}, &pl); err == nil {
			pipeline = &pl
		}
	}

	// List all ReleaseTargets for this release.
	var allTargets kaprov1alpha1.ReleaseTargetList
	if err := s.Client.List(ctx, &allTargets); err != nil {
		http.Error(w, "failed to list targets", http.StatusInternalServerError)
		return
	}
	targets := make([]kaprov1alpha1.ReleaseTarget, 0)
	for _, t := range allTargets.Items {
		if t.Spec.ReleaseRef == releaseName {
			targets = append(targets, t)
		}
	}

	writeJSON(w, http.StatusOK, ReleaseContext{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Release:     release,
		Pipeline:    pipeline,
		Targets:     targets,
	})
}

func (s *Server) handleGateContext(w http.ResponseWriter, r *http.Request, releaseName, targetKey string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var release kaprov1alpha1.Release
	if err := s.Client.Get(ctx, client.ObjectKey{Name: releaseName}, &release); err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return
	}

	var target kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.ReleaseRef != releaseName {
		http.Error(w, "target/release mismatch", http.StatusConflict)
		return
	}

	// Fetch cluster health.
	var cluster *kaprov1alpha1.MemberCluster
	var mc kaprov1alpha1.MemberCluster
	if err := s.Client.Get(ctx, client.ObjectKey{Name: target.Spec.Target}, &mc); err == nil {
		cluster = &mc
	}

	writeJSON(w, http.StatusOK, GateContext{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Target:      target,
		Release:     release,
		Cluster:     cluster,
	})
}

func (s *Server) handleClusterHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clusterName := strings.TrimPrefix(r.URL.Path, "/api/v1/clusters/")
	clusterName = strings.TrimSuffix(clusterName, "/health")
	if clusterName == "" {
		http.Error(w, "cluster name required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var mc kaprov1alpha1.MemberCluster
	if err := s.Client.Get(ctx, client.ObjectKey{Name: clusterName}, &mc); err != nil {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":            mc.Name,
		"labels":          mc.Labels,
		"phase":           mc.Status.Phase,
		"lastHeartbeat":   mc.Status.LastHeartbeat,
		"health":          mc.Status.Health,
		"currentVersions": mc.Status.CurrentVersions,
		"activeRelease":   mc.Status.ActiveRelease,
		"capabilities":    mc.Status.Capabilities,
	})
}

// --- Decision Handler ---

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request, releaseName, targetKey string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	l := log.FromContext(ctx)

	// Parse request body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var req DecisionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate required fields.
	if req.Decision == "" || req.IdempotencyKey == "" {
		http.Error(w, "decision and idempotencyKey are required", http.StatusBadRequest)
		return
	}
	switch req.Decision {
	case "Approve", "Reject", "Defer":
	default:
		http.Error(w, "decision must be Approve, Reject, or Defer", http.StatusBadRequest)
		return
	}

	// Look up release and target.
	var release kaprov1alpha1.Release
	if err := s.Client.Get(ctx, client.ObjectKey{Name: releaseName}, &release); err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return
	}
	if release.Spec.Suspended {
		http.Error(w, "release is suspended", http.StatusConflict)
		return
	}

	var target kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.ReleaseRef != releaseName {
		http.Error(w, "target/release mismatch", http.StatusConflict)
		return
	}

	// Target must be in WaitingApproval to accept a decision.
	if target.Status.Phase != kaprov1alpha1.TargetPhaseWaitingApproval {
		writeJSON(w, http.StatusUnprocessableEntity, DecisionResponse{
			Accepted: false,
			Reason:   fmt.Sprintf("target is in %s, not WaitingApproval", target.Status.Phase),
		})
		return
	}

	// Idempotency check: if a decision with this key already exists, return it.
	if target.Status.DecisionTrace != nil && target.Status.DecisionTrace.Current != nil {
		existing := target.Status.DecisionTrace.Current
		if existing.DecisionID == req.IdempotencyKey {
			if existing.Decision == req.Decision {
				writeJSON(w, http.StatusOK, DecisionResponse{
					Accepted:          true,
					DecisionID:        existing.DecisionID,
					EffectiveDecision: existing.EffectiveDecision,
					Reason:            "idempotent replay",
				})
				return
			}
			// Same key, different decision — conflict.
			writeJSON(w, http.StatusConflict, DecisionResponse{
				Accepted:          false,
				DecisionID:        existing.DecisionID,
				EffectiveDecision: existing.EffectiveDecision,
				Reason:            "idempotencyKey already used with different decision",
			})
			return
		}
		// Different key, decision already exists — conflict (first decision wins).
		writeJSON(w, http.StatusConflict, DecisionResponse{
			Accepted:          false,
			DecisionID:        existing.DecisionID,
			EffectiveDecision: existing.EffectiveDecision,
			Reason:            fmt.Sprintf("target already has decision from %s", existing.Identity.Name),
		})
		return
	}

	// Build the decision entry.
	now := time.Now().UTC().Format(time.RFC3339)
	agentName := extractAgentName(r)
	jwtFP := extractJWTFingerprint(r)

	// Resolve and enforce AgentPolicy if one exists.
	effectiveDecision := req.Decision
	trustLevel := "none"

	policy, err := s.resolveAgentPolicy(ctx, agentName)
	if err != nil {
		l.Error(err, "failed to resolve AgentPolicy")
	}
	if policy != nil {
		// Fetch cluster for label-based checks.
		var mc kaprov1alpha1.MemberCluster
		var cluster *kaprov1alpha1.MemberCluster
		if err := s.Client.Get(ctx, client.ObjectKey{Name: target.Spec.Target}, &mc); err == nil {
			cluster = &mc
		}
		pd := enforceAgentPolicy(policy, &target, cluster, req.Confidence, len(req.Reasoning))
		if !pd.Allowed {
			writeJSON(w, http.StatusForbidden, DecisionResponse{
				Accepted: false,
				Reason:   fmt.Sprintf("AgentPolicy denied: %s", pd.DenyReason),
			})
			return
		}
		trustLevel = string(pd.EffectiveMode)
		if pd.EffectiveMode == kaprov1alpha1.AgentPolicyModeRecommend {
			effectiveDecision = "Recommended"
		}
		if pd.RequireHumanCosign && req.Decision == "Approve" {
			effectiveDecision = "PendingHumanConfirm"
		}
	}

	entry := kaprov1alpha1.DecisionEntry{
		DecisionID:        req.IdempotencyKey,
		Decision:          req.Decision,
		EffectiveDecision: effectiveDecision,
		Identity: kaprov1alpha1.DecisionIdentity{
			Name:           agentName,
			Type:           "ServiceAccount",
			TrustLevel:     trustLevel,
			JWTFingerprint: jwtFP,
		},
		Confidence: req.Confidence,
		Reasoning:  req.Reasoning,
		Factors:    req.Factors,
		Conditions: req.Conditions,
		DecidedAt:  now,
		ExpiresAt:  req.ExpiresAt,
	}

	// Write DecisionTrace to ReleaseTarget.status using MergeFrom patch
	// to avoid conflicting with ReleaseTargetReconciler's status writes.
	patch := client.MergeFrom(target.DeepCopy())
	if target.Status.DecisionTrace == nil {
		target.Status.DecisionTrace = &kaprov1alpha1.DecisionTrace{}
	}

	// Move current to history if exists.
	if target.Status.DecisionTrace.Current != nil {
		prev := *target.Status.DecisionTrace.Current
		prev.SupersededBy = entry.DecisionID
		prev.SupersededReason = "NewDecision"
		target.Status.DecisionTrace.History = append(target.Status.DecisionTrace.History, prev)
		if len(target.Status.DecisionTrace.History) > maxDecisionTraceHistory {
			target.Status.DecisionTrace.History = target.Status.DecisionTrace.History[len(target.Status.DecisionTrace.History)-maxDecisionTraceHistory:]
		}
	}
	target.Status.DecisionTrace.Current = &entry

	if err := s.Client.Status().Patch(ctx, &target, patch); err != nil {
		l.Error(err, "failed to patch DecisionTrace")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	l.Info("Decision recorded",
		"release", releaseName, "target", targetKey,
		"decision", req.Decision, "confidence", req.Confidence,
		"agent", agentName)

	// If decision is Approve and effective mode allows it, create an Approval CR.
	// Recommend mode and PendingHumanConfirm do NOT auto-create Approvals.
	if req.Decision == "Approve" && effectiveDecision == "Approve" {
		approval := &kaprov1alpha1.Approval{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%s", releaseName, targetKey),
			},
			Spec: kaprov1alpha1.ApprovalSpec{
				Release:    releaseName,
				Target:     target.Spec.Target,
				Ref:        targetKey,
				ApprovedBy: fmt.Sprintf("agent:%s", agentName),
				Comment:    fmt.Sprintf("confidence: %.2f\n%s", req.Confidence, req.Reasoning),
			},
		}
		if err := s.Client.Create(ctx, approval); err != nil {
			if !isAlreadyExists(err) {
				l.Error(err, "failed to create Approval from decision")
				http.Error(w, "internal error creating approval", http.StatusInternalServerError)
				return
			}
		}
	}

	// Update AgentPolicy status counters.
	if policy != nil {
		s.updateAgentPolicyStatus(ctx, policy)
	}

	writeJSON(w, http.StatusOK, DecisionResponse{
		Accepted:          true,
		DecisionID:        entry.DecisionID,
		EffectiveDecision: entry.EffectiveDecision,
		Reason:            "decision recorded",
	})
}

// --- Override Handler ---

func (s *Server) handleOverride(w http.ResponseWriter, r *http.Request, releaseName, targetKey string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var req OverrideRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Action == "" || req.Identity == "" || req.Reason == "" {
		http.Error(w, "action, identity, and reason are required", http.StatusBadRequest)
		return
	}

	var target kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.ReleaseRef != releaseName {
		http.Error(w, "target/release mismatch", http.StatusConflict)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	overriddenDecisionID := ""
	if target.Status.DecisionTrace != nil && target.Status.DecisionTrace.Current != nil {
		overriddenDecisionID = target.Status.DecisionTrace.Current.DecisionID
	}

	override := kaprov1alpha1.HumanOverride{
		OverrideID:           fmt.Sprintf("o-%s-%s", time.Now().Format("20060102-150405"), targetKey),
		OverriddenDecisionID: overriddenDecisionID,
		Action:               req.Action,
		Identity:             req.Identity,
		Reason:               req.Reason,
		OverriddenAt:         now,
	}

	patch := client.MergeFrom(target.DeepCopy())
	if target.Status.DecisionTrace == nil {
		target.Status.DecisionTrace = &kaprov1alpha1.DecisionTrace{}
	}
	target.Status.DecisionTrace.HumanOverrides = append(target.Status.DecisionTrace.HumanOverrides, override)

	if err := s.Client.Status().Patch(ctx, &target, patch); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// If override action is Approve, create Approval CR.
	if req.Action == "Approve" {
		approval := &kaprov1alpha1.Approval{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%s", releaseName, targetKey),
			},
			Spec: kaprov1alpha1.ApprovalSpec{
				Release:    releaseName,
				Target:     target.Spec.Target,
				Ref:        targetKey,
				ApprovedBy: req.Identity,
				Comment:    fmt.Sprintf("human override: %s", req.Reason),
			},
		}
		if err := s.Client.Create(ctx, approval); err != nil && !isAlreadyExists(err) {
			log.FromContext(ctx).Error(err, "failed to create Approval from override")
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "override recorded",
		"overrideId": override.OverrideID,
		"action":     req.Action,
	})
}

// --- Helpers ---

// extractAgentName gets the agent identity from the Authorization header.
// In v0.3, we use a simple bearer token extraction. In v0.4, this will
// resolve against AgentPolicy via full JWT validation.
func extractAgentName(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		// For now, use X-Agent-Name header as the identity.
		// Full JWT validation comes with AgentPolicy in v0.4.
		if name := r.Header.Get("X-Agent-Name"); name != "" {
			return name
		}
		return "unknown-agent"
	}
	if name := r.Header.Get("X-Agent-Name"); name != "" {
		return name
	}
	return "anonymous"
}

// extractJWTFingerprint computes a SHA-256 fingerprint of the bearer token
// for audit trail purposes (without storing the token itself).
func extractJWTFingerprint(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		hash := sha256.Sum256([]byte(token))
		return fmt.Sprintf("sha256:%x", hash[:8])
	}
	return ""
}
