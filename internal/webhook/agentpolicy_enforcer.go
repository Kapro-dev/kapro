package webhook

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PolicyDecision is the result of evaluating an AgentPolicy against a decision.
type PolicyDecision struct {
	Allowed            bool
	EffectiveMode      kaprov1alpha1.AgentPolicyMode
	EffectiveMinConf   float64
	DenyReason         string
	RequireHumanCosign bool
}

// resolveAgentPolicy finds the highest-priority AgentPolicy matching the agent name.
func (s *Server) resolveAgentPolicy(ctx context.Context, agentName string) (*kaprov1alpha1.AgentPolicy, error) {
	var list kaprov1alpha1.AgentPolicyList
	if err := s.Client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list AgentPolicies: %w", err)
	}
	var best *kaprov1alpha1.AgentPolicy
	for i := range list.Items {
		ap := &list.Items[i]
		if ap.Spec.Identity.ServiceAccountName == agentName {
			if best == nil || ap.Spec.Priority < best.Spec.Priority {
				best = ap
			}
		}
	}
	return best, nil
}

// enforceAgentPolicy validates a decision against the resolved AgentPolicy.
// Returns a PolicyDecision indicating whether the decision is allowed.
func enforceAgentPolicy(
	policy *kaprov1alpha1.AgentPolicy,
	target *kaprov1alpha1.PromotionTarget,
	cluster *kaprov1alpha1.FleetCluster,
	confidence float64,
	reasoningLen int,
) PolicyDecision {
	if policy == nil {
		// No policy found — deny by default.
		return PolicyDecision{Allowed: false, DenyReason: "no AgentPolicy found for this agent identity"}
	}
	if policy.Spec.Suspended {
		return PolicyDecision{Allowed: false, DenyReason: "AgentPolicy is suspended"}
	}

	// Check mode.
	mode := policy.Spec.Mode
	if mode == kaprov1alpha1.AgentPolicyModeDisabled {
		return PolicyDecision{Allowed: false, DenyReason: "AgentPolicy mode is disabled"}
	}

	// Check stage scope.
	stage := target.Spec.Stage
	if !isStageAllowed(policy.Spec.Scope, stage) {
		return PolicyDecision{Allowed: false, DenyReason: fmt.Sprintf("stage %q is not in scope (or is excluded)", stage)}
	}

	// Check cluster exclusion.
	clusterName := target.Spec.Target
	if slices.Contains(policy.Spec.Scope.ExcludeClusters, clusterName) {
		return PolicyDecision{Allowed: false, DenyReason: fmt.Sprintf("cluster %q is in excludeClusters", clusterName)}
	}

	// Check cluster selector.
	if policy.Spec.Scope.ClusterSelector != nil && cluster != nil {
		sel, err := metav1.LabelSelectorAsSelector(policy.Spec.Scope.ClusterSelector)
		if err == nil && !sel.Matches(labels.Set(cluster.Labels)) {
			return PolicyDecision{Allowed: false, DenyReason: "cluster labels do not match clusterSelector"}
		}
	}

	// Resolve effective confidence threshold.
	effectiveMinConf := policy.Spec.Confidence.Default

	// Tier override.
	if cluster != nil {
		tier := cluster.Labels["kapro.io/tier"]
		if tier == "" {
			tier = cluster.Labels["tier"]
		}
		if v, ok := policy.Spec.Confidence.TierOverrides[tier]; ok && v > effectiveMinConf {
			effectiveMinConf = v
		}
	}

	// Stage override.
	if v, ok := policy.Spec.Confidence.StageOverrides[stage]; ok && v > effectiveMinConf {
		effectiveMinConf = v
	}

	// Country profile override.
	requireHumanCosign := false
	if cluster != nil {
		country := cluster.Labels["kapro.io/country"]
		if country == "" {
			country = cluster.Labels["country"]
		}
		for _, cp := range policy.Spec.Scope.CountryProfiles {
			if slices.Contains(cp.Countries, country) {
				if cp.MinConfidence > effectiveMinConf {
					effectiveMinConf = cp.MinConfidence
				}
				if cp.Mode != nil {
					mode = *cp.Mode
				}
				if cp.RequireHumanCosign {
					requireHumanCosign = true
				}
				break
			}
		}
	}

	// Validate confidence.
	if confidence < effectiveMinConf {
		return PolicyDecision{
			Allowed:          false,
			EffectiveMinConf: effectiveMinConf,
			DenyReason:       fmt.Sprintf("confidence %.2f below threshold %.2f", confidence, effectiveMinConf),
		}
	}

	// Validate audit requirements.
	if policy.Spec.Audit.RequireReasoning && reasoningLen == 0 {
		return PolicyDecision{Allowed: false, DenyReason: "reasoning is required but empty"}
	}
	if policy.Spec.Audit.MinReasoningLength > 0 && int32(reasoningLen) < policy.Spec.Audit.MinReasoningLength {
		return PolicyDecision{
			Allowed:    false,
			DenyReason: fmt.Sprintf("reasoning length %d below minimum %d", reasoningLen, policy.Spec.Audit.MinReasoningLength),
		}
	}

	// Validate time windows.
	if !isWithinTimeWindows(policy.Spec.TimeWindows) {
		return PolicyDecision{Allowed: false, DenyReason: "decision outside allowed time window"}
	}

	// Validate rate limits against status counters.
	if policy.Spec.RateLimits != nil {
		rl := policy.Spec.RateLimits
		if rl.MaxApprovalsPerDay > 0 && policy.Status.DecisionsToday >= rl.MaxApprovalsPerDay {
			return PolicyDecision{Allowed: false, DenyReason: "daily approval limit reached"}
		}
		if rl.MaxConcurrent > 0 && policy.Status.ActiveDecisions >= rl.MaxConcurrent {
			return PolicyDecision{Allowed: false, DenyReason: "concurrent approval limit reached"}
		}
		if rl.Cooldown != "" && policy.Status.LastDecisionAt != "" {
			cooldown, err := time.ParseDuration(rl.Cooldown)
			if err == nil {
				last, parseErr := time.Parse(time.RFC3339, policy.Status.LastDecisionAt)
				if parseErr == nil && time.Since(last) < cooldown {
					return PolicyDecision{Allowed: false, DenyReason: fmt.Sprintf("cooldown period not elapsed (last: %s)", policy.Status.LastDecisionAt)}
				}
			}
		}
	}

	return PolicyDecision{
		Allowed:            true,
		EffectiveMode:      mode,
		EffectiveMinConf:   effectiveMinConf,
		RequireHumanCosign: requireHumanCosign,
	}
}

func isStageAllowed(scope kaprov1alpha1.AgentScope, stage string) bool {
	// ExcludeStages always wins.
	if slices.Contains(scope.ExcludeStages, stage) {
		return false
	}
	// If Stages is empty, all stages are allowed.
	if len(scope.Stages) == 0 {
		return true
	}
	return slices.Contains(scope.Stages, stage)
}

func isWithinTimeWindows(windows []kaprov1alpha1.AgentTimeWindow) bool {
	if len(windows) == 0 {
		return true
	}

	now := time.Now()

	for _, w := range windows {
		loc, err := time.LoadLocation(w.Timezone)
		if err != nil {
			loc = time.UTC
		}
		localNow := now.In(loc)

		// Check day of week.
		if len(w.DaysOfWeek) > 0 {
			dayStr := strings.ToLower(localNow.Weekday().String()[:3])
			if !slices.Contains(w.DaysOfWeek, dayStr) {
				if w.Deny {
					continue // deny window doesn't apply today
				}
				return false // allow window doesn't apply today
			}
		}

		// Parse start/end times.
		start, err1 := time.Parse("15:04", w.StartTime)
		end, err2 := time.Parse("15:04", w.EndTime)
		if err1 != nil || err2 != nil {
			continue
		}

		currentMinutes := localNow.Hour()*60 + localNow.Minute()
		startMinutes := start.Hour()*60 + start.Minute()
		endMinutes := end.Hour()*60 + end.Minute()

		inWindow := currentMinutes >= startMinutes && currentMinutes <= endMinutes

		if w.Deny && inWindow {
			return false // blocked by deny window
		}
		if !w.Deny && inWindow {
			return true // within allow window
		}
	}

	// If only deny windows were defined and none matched, allow.
	hasAllowWindow := false
	for _, w := range windows {
		if !w.Deny {
			hasAllowWindow = true
			break
		}
	}
	if hasAllowWindow {
		return false // had allow windows but none matched
	}
	return true // only deny windows, none blocked
}

// updateAgentPolicyStatus increments the decision counters on the AgentPolicy.
func (s *Server) updateAgentPolicyStatus(ctx context.Context, policy *kaprov1alpha1.AgentPolicy) {
	patch := client.MergeFrom(policy.DeepCopy())
	policy.Status.ActiveDecisions++
	policy.Status.DecisionsToday++
	policy.Status.LastDecisionAt = time.Now().UTC().Format(time.RFC3339)
	policy.Status.ObservedGeneration = policy.Generation
	_ = s.Client.Status().Patch(ctx, policy, patch)
}
