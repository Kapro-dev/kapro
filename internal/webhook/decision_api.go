// Package webhook — Decision API endpoints for AI-native progressive delivery.
//
// The Decision API extends the existing webhook server with endpoints that
// allow AI agents to query fleet context and submit deployment decisions.
// All endpoints are mounted under /api/v1/ and authenticated with Kubernetes bearer tokens.
//
// Context endpoints (read-only):
//
//	GET /api/v1/fleet                                    — fleet-wide summary
//	GET /api/v1/promotionruns/{name}/context                  — full promotionrun context
//	GET /api/v1/promotionruns/{name}/targets/{key}/gate       — gate evaluation context
//	GET /api/v1/clusters/{name}/health                   — cluster health
//
// Decision endpoints (mutating):
//
//	POST /api/v1/promotionruns/{name}/targets/{key}/decide    — submit a decision
//	POST /api/v1/promotionruns/{name}/targets/{key}/override  — human override
package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	maxDecisionTraceHistory = 10
	defaultDecisionAPILimit = 100
	maxDecisionAPILimit     = 500

	// maxDecisionReasoningLen bounds the free-text Reasoning field on a
	// DecisionRequest after JSON unmarshal. 8 KiB is enough for a thorough
	// rationale; pathological inputs packed into the 64 KiB body limit are
	// rejected up front (webhook hardening, gate sprint).
	maxDecisionReasoningLen = 8 * 1024

	// maxIdempotencyKeyLen bounds the IdempotencyKey field. Real keys are
	// short ULIDs / UUIDs (< 64 bytes); 256 leaves headroom without abuse.
	maxIdempotencyKeyLen = 256
	// Phase is not a selectable field for every Kapro API. Bound sparse phase
	// scans to a multiple of the requested response limit and return a truncated
	// partial view when the scan budget is exhausted.
	decisionAPIScanLimitMultiplier = 10

	decisionPromotionRunLabel = "kapro.io/promotionrun"
	decisionPhaseLabel        = "kapro.io/phase"
)

// RegisterDecisionAPI mounts the Decision API endpoints on the given mux.
func (s *Server) RegisterDecisionAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/fleet", s.handleFleet)
	mux.HandleFunc("/api/v1/promotionruns/", s.routePromotionRun)
	mux.HandleFunc("/api/v1/clusters/", s.handleClusterHealth)
}

// --- Fleet Context ---

// FleetSummary is the response for GET /api/v1/fleet.
type FleetSummary struct {
	GeneratedAt         string                `json:"generatedAt"`
	TotalClusters       int                   `json:"totalClusters"`
	HealthyClusters     int                   `json:"healthyClusters"`
	DegradedClusters    int                   `json:"degradedClusters"`
	ActivePromotionRuns int                   `json:"activePromotionRuns"`
	PendingDecisions    int                   `json:"pendingDecisions"`
	Clusters            []ClusterSummary      `json:"clusters"`
	PromotionRuns       []PromotionRunSummary `json:"promotionruns"`
	Page                DecisionAPIPage       `json:"page"`
}

// DecisionAPIPage describes bounded Decision API read responses.
type DecisionAPIPage struct {
	Limit         int            `json:"limit"`
	LabelSelector string         `json:"labelSelector,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	Truncated     bool           `json:"truncated"`
	Counts        map[string]int `json:"counts,omitempty"`
}

type decisionListOptions struct {
	limit         int
	labelSelector string
	selector      labels.Selector
	phase         string
}

func (s *Server) decisionReader() client.Reader {
	if s.DecisionReader != nil {
		return s.DecisionReader
	}
	return s.Client
}

// ClusterSummary is a compact view of one FleetCluster.
type ClusterSummary struct {
	Name          string            `json:"name"`
	Labels        map[string]string `json:"labels"`
	Phase         string            `json:"phase"`
	Healthy       bool              `json:"healthy"`
	LastHeartbeat string            `json:"lastHeartbeat,omitempty"`
	Versions      map[string]string `json:"versions,omitempty"`
}

// PromotionRunSummary is a compact view of one PromotionRun.
type PromotionRunSummary struct {
	Name          string `json:"name"`
	Phase         string `json:"phase"`
	Plan string `json:"promotionplan,omitempty"`
	StartedAt     string `json:"startedAt,omitempty"`
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, ok := s.requireDecisionAccess(ctx, w, r,
		kaproAttrs("list", "fleetclusters", ""),
		kaproAttrs("list", "promotionruns", ""),
		kaproAttrs("list", "promotiontargets", ""),
	); !ok {
		return
	}
	opts, err := decisionListOptionsFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	clusters, clusterCount, clustersTruncated, err := listDecisionItems(
		ctx,
		s.decisionReader(),
		opts,
		func() client.ObjectList { return &kaprov1alpha2.ClusterList{} },
		func(list client.ObjectList) []kaprov1alpha2.Cluster {
			return list.(*kaprov1alpha2.ClusterList).Items
		},
		filterDecisionFleetClustersByPhase,
	)
	if err != nil {
		http.Error(w, "failed to list clusters", http.StatusInternalServerError)
		return
	}

	promotionruns, promotionRunCount, promotionRunsTruncated, err := listDecisionItems(
		ctx,
		s.decisionReader(),
		opts,
		func() client.ObjectList { return &kaprov1alpha2.PromotionRunList{} },
		func(list client.ObjectList) []kaprov1alpha2.PromotionRun {
			return list.(*kaprov1alpha2.PromotionRunList).Items
		},
		filterDecisionPromotionRunsByPhase,
	)
	if err != nil {
		http.Error(w, "failed to list promotionruns", http.StatusInternalServerError)
		return
	}

	var targets []kaprov1alpha2.Target
	pendingDecisionCount, pendingDecisionsTruncated := 0, false
	if opts.phase == "" || strings.EqualFold(string(kaprov1alpha2.TargetPhaseWaitingApproval), opts.phase) {
		// Count pending decisions through the same bounded read path so one large
		// fleet cannot force the Decision API to materialize every target.
		var err error
		targets, pendingDecisionCount, pendingDecisionsTruncated, err = listDecisionItems(
			ctx,
			s.decisionReader(),
			opts,
			func() client.ObjectList { return &kaprov1alpha2.TargetList{} },
			func(list client.ObjectList) []kaprov1alpha2.Target {
				return list.(*kaprov1alpha2.TargetList).Items
			},
			filterDecisionPendingTargetsByPhase,
		)
		if err != nil {
			http.Error(w, "failed to list targets", http.StatusInternalServerError)
			return
		}
	}

	healthy, degraded, pendingDecisions := 0, 0, 0
	for _, mc := range clusters {
		if mc.Status.Health.AllWorkloadsReady {
			healthy++
		} else {
			degraded++
		}
	}
	for _, t := range targets {
		if t.Status.Phase == kaprov1alpha2.TargetPhaseWaitingApproval {
			pendingDecisions++
		}
	}

	clusterSummaries := make([]ClusterSummary, 0, len(clusters))
	for _, mc := range clusters {
		clusterSummaries = append(clusterSummaries, ClusterSummary{
			Name:          mc.Name,
			Labels:        mc.Labels,
			Phase:         string(mc.Status.Phase),
			Healthy:       mc.Status.Health.AllWorkloadsReady,
			LastHeartbeat: mc.Status.LastHeartbeat,
			Versions:      mc.Status.CurrentVersions,
		})
	}

	activePromotionRuns := 0
	promotionrunSummaries := make([]PromotionRunSummary, 0, len(promotionruns))
	for _, rel := range promotionruns {
		if rel.Status.Phase == kaprov1alpha2.PromotionRunPhaseProgressing {
			activePromotionRuns++
		}
		promotionplan := ""
		if len(rel.Spec.PromotionPlans) > 0 {
			promotionplan = rel.Spec.PromotionPlans[0].Plan
		}
		promotionrunSummaries = append(promotionrunSummaries, PromotionRunSummary{
			Name:          rel.Name,
			Phase:         string(rel.Status.Phase),
			Plan: promotionplan,
			StartedAt:     rel.Status.StartedAt,
		})
	}

	writeJSON(w, http.StatusOK, FleetSummary{
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		TotalClusters:       len(clusters),
		HealthyClusters:     healthy,
		DegradedClusters:    degraded,
		ActivePromotionRuns: activePromotionRuns,
		PendingDecisions:    pendingDecisions,
		Clusters:            clusterSummaries,
		PromotionRuns:       promotionrunSummaries,
		Page: DecisionAPIPage{
			Limit:         opts.limit,
			LabelSelector: opts.labelSelector,
			Phase:         opts.phase,
			Truncated:     clustersTruncated || promotionRunsTruncated || pendingDecisionsTruncated,
			Counts: map[string]int{
				"fleetclusters":    clusterCount,
				"promotionruns":    promotionRunCount,
				"pendingdecisions": pendingDecisionCount,
			},
		},
	})
}

// --- PromotionRun Context ---

// PromotionRunContext is the response for GET /api/v1/promotionruns/{name}/context.
type PromotionRunContext struct {
	GeneratedAt   string                          `json:"generatedAt"`
	PromotionRun  kaprov1alpha2.PromotionRun      `json:"promotionrun"`
	Plan *kaprov1alpha2.Plan    `json:"promotionplan,omitempty"`
	Targets       []kaprov1alpha2.Target `json:"targets"`
	Page          DecisionAPIPage                 `json:"page"`
}

// --- Gate Context ---

// GateContext is the response for GET /api/v1/promotionruns/{name}/targets/{key}/gate.
type GateContext struct {
	GeneratedAt  string                        `json:"generatedAt"`
	Target       kaprov1alpha2.Target `json:"target"`
	PromotionRun kaprov1alpha2.PromotionRun    `json:"promotionrun"`
	Cluster      *kaprov1alpha2.Cluster   `json:"cluster,omitempty"`
	Precedents   []DecisionPrecedent           `json:"precedents,omitempty"`
}

// DecisionPrecedent is a historical decision on this target for agent learning.
type DecisionPrecedent struct {
	PromotionRun string  `json:"promotionrun"`
	Decision     string  `json:"decision"`
	Confidence   float64 `json:"confidence"`
	Outcome      string  `json:"outcome"`
	Agent        string  `json:"agent"`
}

// --- Decision Request/Response ---

// DecisionRequest is the payload for POST /api/v1/promotionruns/{name}/targets/{key}/decide.
type DecisionRequest struct {
	Decision       string                            `json:"decision"`
	Confidence     float64                           `json:"confidence"`
	Reasoning      string                            `json:"reasoning"`
	Factors        []kaprov1alpha2.DecisionFactor    `json:"factors,omitempty"`
	Conditions     []kaprov1alpha2.DecisionCondition `json:"conditions,omitempty"`
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

func (s *Server) routePromotionRun(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/promotionruns/{name}/context
	//        /api/v1/promotionruns/{name}/targets/{key}/gate
	//        /api/v1/promotionruns/{name}/targets/{key}/decide
	//        /api/v1/promotionruns/{name}/targets/{key}/override
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/promotionruns/")
	parts := strings.Split(path, "/")

	switch {
	case len(parts) == 2 && parts[1] == "context":
		s.handlePromotionRunContext(w, r, parts[0])
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

func (s *Server) handlePromotionRunContext(w http.ResponseWriter, r *http.Request, promotionrunName string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, ok := s.requireDecisionAccess(ctx, w, r,
		kaproAttrs("get", "promotionruns", promotionrunName),
		kaproAttrs("list", "promotiontargets", ""),
	); !ok {
		return
	}
	opts, err := decisionListOptionsFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var promotionrun kaprov1alpha2.PromotionRun
	if err := s.decisionReader().Get(ctx, client.ObjectKey{Name: promotionrunName}, &promotionrun); err != nil {
		http.Error(w, "promotionrun not found", http.StatusNotFound)
		return
	}

	// Resolve the first promotionplan for context.
	var promotionplan *kaprov1alpha2.Plan
	if len(promotionrun.Spec.PromotionPlans) > 0 {
		var pl kaprov1alpha2.Plan
		if err := s.decisionReader().Get(ctx, client.ObjectKey{Name: promotionrun.Spec.PromotionPlans[0].Plan}, &pl); err == nil {
			promotionplan = &pl
		}
	}

	targets, targetCount, targetsTruncated, err := listDecisionItems(
		ctx,
		s.decisionReader(),
		decisionOptionsWithPromotionRunSelector(opts, promotionrunName),
		func() client.ObjectList { return &kaprov1alpha2.TargetList{} },
		func(list client.ObjectList) []kaprov1alpha2.Target {
			return list.(*kaprov1alpha2.TargetList).Items
		},
		func(items []kaprov1alpha2.Target, phase string) []kaprov1alpha2.Target {
			return filterDecisionPromotionTargetsForRunByPhase(items, promotionrunName, phase)
		},
	)
	if err != nil {
		http.Error(w, "failed to list targets", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, PromotionRunContext{
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		PromotionRun:  promotionrun,
		Plan: promotionplan,
		Targets:       targets,
		Page: DecisionAPIPage{
			Limit:         opts.limit,
			LabelSelector: opts.labelSelector,
			Phase:         opts.phase,
			Truncated:     targetsTruncated,
			Counts:        map[string]int{"promotiontargets": targetCount},
		},
	})
}

func decisionListOptionsFromRequest(r *http.Request) (decisionListOptions, error) {
	q := r.URL.Query()
	limit := defaultDecisionAPILimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxDecisionAPILimit {
			return decisionListOptions{}, fmt.Errorf("limit must be an integer between 1 and %d", maxDecisionAPILimit)
		}
		limit = parsed
	}

	selector := labels.Everything()
	labelSelector := q.Get("labelSelector")
	if labelSelector != "" {
		parsed, err := labels.Parse(labelSelector)
		if err != nil {
			return decisionListOptions{}, fmt.Errorf("labelSelector is invalid: %w", err)
		}
		selector = parsed
	}

	return decisionListOptions{
		limit:         limit,
		labelSelector: labelSelector,
		selector:      selector,
		phase:         q.Get("phase"),
	}, nil
}

func decisionOptionsWithPromotionRunSelector(opts decisionListOptions, promotionrunName string) decisionListOptions {
	req, err := labels.NewRequirement(decisionPromotionRunLabel, selection.Equals, []string{promotionrunName})
	if err != nil {
		return opts
	}
	opts.selector = opts.selector.Add(*req)
	return opts
}

func listDecisionItems[T any](
	ctx context.Context,
	c client.Reader,
	opts decisionListOptions,
	newList func() client.ObjectList,
	itemsFromList func(client.ObjectList) []T,
	filter func([]T, string) []T,
) ([]T, int, bool, error) {
	var out []T
	continueToken := ""
	pageSize := int64(opts.limit + 1)
	scanLimit := opts.limit * decisionAPIScanLimitMultiplier
	if scanLimit < opts.limit+1 {
		scanLimit = opts.limit + 1
	}
	scanned := 0
	for {
		list := newList()
		remainingScanBudget := scanLimit - scanned
		if remainingScanBudget <= 0 {
			return out, len(out), true, nil
		}
		thisPageSize := pageSize
		if int64(remainingScanBudget) < thisPageSize {
			thisPageSize = int64(remainingScanBudget)
		}
		listOpts := []client.ListOption{
			client.MatchingLabelsSelector{Selector: opts.selector},
			client.Limit(thisPageSize),
		}
		if continueToken != "" {
			listOpts = append(listOpts, client.Continue(continueToken))
		}
		if err := c.List(ctx, list, listOpts...); err != nil {
			return nil, 0, false, err
		}

		pageItems := itemsFromList(list)
		scanned += len(pageItems)
		out = append(out, filter(pageItems, opts.phase)...)
		if len(out) > opts.limit {
			return out[:opts.limit], opts.limit, true, nil
		}
		if scanned > scanLimit {
			return out, len(out), true, nil
		}

		continueToken = list.GetContinue()
		if continueToken == "" {
			return out, len(out), false, nil
		}
		if scanned >= scanLimit {
			return out, len(out), true, nil
		}
	}
}

func filterDecisionFleetClustersByPhase(items []kaprov1alpha2.Cluster, phase string) []kaprov1alpha2.Cluster {
	if phase == "" {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if strings.EqualFold(string(item.Status.Phase), phase) {
			out = append(out, item)
		}
	}
	return out
}

func filterDecisionPromotionRunsByPhase(items []kaprov1alpha2.PromotionRun, phase string) []kaprov1alpha2.PromotionRun {
	if phase == "" {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if strings.EqualFold(string(item.Status.Phase), phase) {
			out = append(out, item)
		}
	}
	return out
}

func filterDecisionPromotionTargetsForRunByPhase(items []kaprov1alpha2.Target, promotionrunName, phase string) []kaprov1alpha2.Target {
	out := items[:0]
	for _, item := range items {
		if item.Spec.PromotionRunRef != promotionrunName {
			continue
		}
		if phase != "" && !strings.EqualFold(string(item.Status.Phase), phase) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func filterDecisionPendingTargetsByPhase(items []kaprov1alpha2.Target, phase string) []kaprov1alpha2.Target {
	if phase != "" && !strings.EqualFold(string(kaprov1alpha2.TargetPhaseWaitingApproval), phase) {
		return nil
	}
	out := items[:0]
	for _, item := range items {
		if item.Status.Phase == kaprov1alpha2.TargetPhaseWaitingApproval {
			out = append(out, item)
		}
	}
	return out
}

func (s *Server) handleGateContext(w http.ResponseWriter, r *http.Request, promotionrunName, targetKey string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, ok := s.requireDecisionAccess(ctx, w, r,
		kaproAttrs("get", "promotionruns", promotionrunName),
		kaproAttrs("get", "promotiontargets", targetKey),
	); !ok {
		return
	}

	var promotionrun kaprov1alpha2.PromotionRun
	if err := s.decisionReader().Get(ctx, client.ObjectKey{Name: promotionrunName}, &promotionrun); err != nil {
		http.Error(w, "promotionrun not found", http.StatusNotFound)
		return
	}

	var target kaprov1alpha2.Target
	if err := s.decisionReader().Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.PromotionRunRef != promotionrunName {
		http.Error(w, "target/promotionrun mismatch", http.StatusConflict)
		return
	}

	// Fetch cluster health.
	var cluster *kaprov1alpha2.Cluster
	var mc kaprov1alpha2.Cluster
	if err := s.decisionReader().Get(ctx, client.ObjectKey{Name: target.Spec.Target}, &mc); err == nil {
		cluster = &mc
	}

	writeJSON(w, http.StatusOK, GateContext{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Target:       target,
		PromotionRun: promotionrun,
		Cluster:      cluster,
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
	if _, ok := s.requireDecisionAccess(ctx, w, r,
		kaproAttrs("get", "fleetclusters", clusterName),
	); !ok {
		return
	}

	var mc kaprov1alpha2.Cluster
	if err := s.decisionReader().Get(ctx, client.ObjectKey{Name: clusterName}, &mc); err != nil {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":               mc.Name,
		"labels":             mc.Labels,
		"phase":              mc.Status.Phase,
		"lastHeartbeat":      mc.Status.LastHeartbeat,
		"health":             mc.Status.Health,
		"currentVersions":    mc.Status.CurrentVersions,
		"activePromotionRun": mc.Status.ActivePromotionRun,
		"capabilities":       mc.Status.Capabilities,
	})
}

// --- Decision Handler ---

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request, promotionrunName, targetKey string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	l := log.FromContext(ctx)
	user, ok := s.requireDecisionAccess(ctx, w, r,
		kaproAttrs("get", "promotionruns", promotionrunName),
		kaproAttrs("get", "promotiontargets", targetKey),
		kaproSubresourceAttrs("patch", "promotiontargets", "status", targetKey),
	)
	if !ok {
		return
	}

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
	// Bound the per-field string lengths after JSON unmarshal so a 64 KiB
	// body cannot be packed into a single Reasoning or IdempotencyKey
	// string and slow down policy enforcement, logging, or status writes
	// downstream (webhook hardening).
	if len(req.Reasoning) > maxDecisionReasoningLen {
		http.Error(w, fmt.Sprintf("reasoning exceeds %d bytes", maxDecisionReasoningLen), http.StatusRequestEntityTooLarge)
		return
	}
	if len(req.IdempotencyKey) > maxIdempotencyKeyLen {
		http.Error(w, fmt.Sprintf("idempotencyKey exceeds %d bytes", maxIdempotencyKeyLen), http.StatusRequestEntityTooLarge)
		return
	}

	// Look up promotionrun and target.
	var promotionrun kaprov1alpha2.PromotionRun
	if err := s.Client.Get(ctx, client.ObjectKey{Name: promotionrunName}, &promotionrun); err != nil {
		http.Error(w, "promotionrun not found", http.StatusNotFound)
		return
	}
	if promotionrun.Spec.Suspended {
		http.Error(w, "promotionrun is suspended", http.StatusConflict)
		return
	}

	var target kaprov1alpha2.Target
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.PromotionRunRef != promotionrunName {
		http.Error(w, "target/promotionrun mismatch", http.StatusConflict)
		return
	}

	// Target must be in WaitingApproval to accept a decision.
	if target.Status.Phase != kaprov1alpha2.TargetPhaseWaitingApproval {
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
	agentName := decisionIdentityName(user)
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
		var mc kaprov1alpha2.Cluster
		var cluster *kaprov1alpha2.Cluster
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
		if pd.EffectiveMode == kaprov1alpha2.AgentPolicyModeRecommend {
			effectiveDecision = "Recommended"
		}
		if pd.RequireHumanCosign && req.Decision == "Approve" {
			effectiveDecision = "PendingHumanConfirm"
		}
	}

	// Security (gate-B1): any "Approve" submission requires create-approvals
	// permission even when AgentPolicy downgrades to Recommended /
	// PendingHumanConfirm. The intent of the user matters, not the
	// post-policy effective form.
	//
	// Run this check BEFORE reserveAgentPolicySlot (review fix gate-v6.1):
	// a rejected SAR should NOT consume a rate-limit slot.
	if req.Decision == "Approve" {
		if err := s.authorizeDecisionUser(ctx, *user, kaproAttrs("create", "approvals", "")); err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	// Reserve a rate-limit slot atomically (gate-B2). This both checks the
	// policy's rate-limit counters and increments them in one CAS pass so N
	// parallel requests cannot all observe the same stale counter and
	// overshoot the configured cap. Runs AFTER all authorization checks
	// (review fix gate-v6.1) so rejected requests don't consume capacity.
	//
	// ActiveDecisions is decremented in a deferred release on every
	// non-2xx exit between here and the final 200; DecisionsToday is
	// intentionally NOT released since it's a daily quota (rolls over
	// in reserveAgentPolicySlot when LastDecisionAt is from a prior UTC day).
	released := false
	if policy != nil {
		ok, denyReason, resErr := s.reserveAgentPolicySlot(ctx, policy)
		if resErr != nil {
			l.Error(resErr, "AgentPolicy rate-limit reservation failed")
			http.Error(w, "internal error reserving agent policy slot", http.StatusInternalServerError)
			return
		}
		if !ok {
			writeJSON(w, http.StatusTooManyRequests, DecisionResponse{
				Accepted: false,
				Reason:   fmt.Sprintf("AgentPolicy denied: %s", denyReason),
			})
			return
		}
		// Defer release: any return after this point WITHOUT a successful
		// decision write must decrement ActiveDecisions. Set released=true
		// once the happy path commits.
		defer func() {
			if !released {
				if relErr := s.releaseAgentPolicySlot(ctx, policy); relErr != nil {
					l.Error(relErr, "AgentPolicy slot release failed; ActiveDecisions may leak")
				}
			}
		}()
	}
	_ = released // assigned in happy path below

	entry := kaprov1alpha2.DecisionEntry{
		DecisionID:        req.IdempotencyKey,
		Decision:          req.Decision,
		EffectiveDecision: effectiveDecision,
		Identity: kaprov1alpha2.DecisionIdentity{
			Name:           agentName,
			Type:           decisionIdentityType(user),
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

	// Write DecisionTrace to PromotionTarget.status using MergeFrom patch
	// to avoid conflicting with PromotionTargetReconciler's status writes.
	patch := client.MergeFrom(target.DeepCopy())
	if target.Status.DecisionTrace == nil {
		target.Status.DecisionTrace = &kaprov1alpha2.DecisionTrace{}
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
		"promotionrun", promotionrunName, "target", targetKey,
		"decision", req.Decision, "confidence", req.Confidence,
		"agent", agentName)

	// If decision is Approve and effective mode allows it, create an Approval CR.
	// Recommend mode and PendingHumanConfirm do NOT auto-create Approvals.
	if req.Decision == "Approve" && effectiveDecision == "Approve" {
		approval := &kaprov1alpha2.Approval{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%s", promotionrunName, targetKey),
			},
			Spec: kaprov1alpha2.ApprovalSpec{
				PromotionRun: promotionrunName,
				Target:       target.Spec.Target,
				Ref:          targetKey,
				ApprovedBy:   fmt.Sprintf("agent:%s", agentName),
				Comment:      fmt.Sprintf("confidence: %.2f\n%s", req.Confidence, req.Reasoning),
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

	// Decision recorded successfully — mark the slot reservation as
	// committed so the deferred release does NOT decrement
	// ActiveDecisions. The slot is decremented later by the
	// Approval/decision-completion controller (TODO v0.7: wire this
	// release path) or by the daily reset; for now we just retain the
	// in-flight count for the duration of the active rollout.
	released = true

	writeJSON(w, http.StatusOK, DecisionResponse{
		Accepted:          true,
		DecisionID:        entry.DecisionID,
		EffectiveDecision: entry.EffectiveDecision,
		Reason:            "decision recorded",
	})
}

// --- Override Handler ---

func (s *Server) handleOverride(w http.ResponseWriter, r *http.Request, promotionrunName, targetKey string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	user, ok := s.requireDecisionAccess(ctx, w, r,
		kaproAttrs("get", "promotionruns", promotionrunName),
		kaproAttrs("get", "promotiontargets", targetKey),
		kaproSubresourceAttrs("patch", "promotiontargets", "status", targetKey),
	)
	if !ok {
		return
	}

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
	if req.Action == "" || req.Reason == "" {
		http.Error(w, "action and reason are required", http.StatusBadRequest)
		return
	}

	var target kaprov1alpha2.Target
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.PromotionRunRef != promotionrunName {
		http.Error(w, "target/promotionrun mismatch", http.StatusConflict)
		return
	}
	if req.Action == "Approve" {
		if err := s.authorizeDecisionUser(ctx, *user, kaproAttrs("create", "approvals", "")); err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	overriddenDecisionID := ""
	if target.Status.DecisionTrace != nil && target.Status.DecisionTrace.Current != nil {
		overriddenDecisionID = target.Status.DecisionTrace.Current.DecisionID
	}

	override := kaprov1alpha2.HumanOverride{
		OverrideID:           fmt.Sprintf("o-%s-%s", time.Now().Format("20060102-150405"), targetKey),
		OverriddenDecisionID: overriddenDecisionID,
		Action:               req.Action,
		Identity:             decisionIdentityName(user),
		Reason:               req.Reason,
		OverriddenAt:         now,
	}

	patch := client.MergeFrom(target.DeepCopy())
	if target.Status.DecisionTrace == nil {
		target.Status.DecisionTrace = &kaprov1alpha2.DecisionTrace{}
	}
	target.Status.DecisionTrace.HumanOverrides = append(target.Status.DecisionTrace.HumanOverrides, override)

	if err := s.Client.Status().Patch(ctx, &target, patch); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// If override action is Approve, create Approval CR.
	if req.Action == "Approve" {
		approval := &kaprov1alpha2.Approval{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%s", promotionrunName, targetKey),
			},
			Spec: kaprov1alpha2.ApprovalSpec{
				PromotionRun: promotionrunName,
				Target:       target.Spec.Target,
				Ref:          targetKey,
				ApprovedBy:   decisionIdentityName(user),
				Comment:      fmt.Sprintf("human override: %s", req.Reason),
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

func decisionIdentityName(user *authnv1.UserInfo) string {
	if user != nil && user.Username != "" {
		return user.Username
	}
	return "unknown"
}

func decisionIdentityType(user *authnv1.UserInfo) string {
	if user == nil {
		return "Unknown"
	}
	switch {
	case strings.HasPrefix(user.Username, "system:serviceaccount:"):
		return "ServiceAccount"
	case strings.HasPrefix(user.Username, "system:node:"):
		return "Node"
	default:
		return "User"
	}
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
