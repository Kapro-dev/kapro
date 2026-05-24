package webhook

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// PolicyDecision is the result of evaluating an AgentPolicy against a decision.
type PolicyDecision struct {
	Allowed            bool
	EffectiveMode      kaprov1alpha2.PolicyMode
	EffectiveMinConf   float64
	DenyReason         string
	RequireHumanCosign bool
}

// resolveAgentPolicy finds the highest-priority AgentPolicy matching the
// authenticated Kubernetes identity.
func (s *Server) resolveAgentPolicy(ctx context.Context, agentName string) (*kaprov1alpha2.Policy, error) {
	var list kaprov1alpha2.PolicyList
	if err := s.decisionReader().List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list AgentPolicies: %w", err)
	}
	saNamespace, saName, isServiceAccount := parseServiceAccountUsername(agentName)
	var best *kaprov1alpha2.Policy
	for i := range list.Items {
		ap := &list.Items[i]
		if isServiceAccount {
			if ap.Spec.Identity.ServiceAccountName != saName {
				continue
			}
			if ap.Spec.Identity.ServiceAccountNamespace != "" && ap.Spec.Identity.ServiceAccountNamespace != saNamespace {
				continue
			}
		} else if ap.Spec.Identity.ServiceAccountName != agentName {
			continue
		}
		if best == nil || ap.Spec.Priority < best.Spec.Priority {
			best = ap
		}
	}
	return best, nil
}

func parseServiceAccountUsername(username string) (string, string, bool) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(username, prefix), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// enforceAgentPolicy validates a decision against the resolved AgentPolicy.
// Returns a PolicyDecision indicating whether the decision is allowed.
func enforceAgentPolicy(
	policy *kaprov1alpha2.Policy,
	target *kaprov1alpha2.Target,
	cluster *kaprov1alpha2.Cluster,
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
	if mode == kaprov1alpha2.PolicyModeDisabled {
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

	// Note: rate limits (MaxApprovalsPerDay, MaxConcurrent, Cooldown) are NOT
	// checked here. They live in reserveAgentPolicySlot, which atomically
	// validates and increments the counter via Status().Update so concurrent
	// decisions cannot all observe the same stale counter and pass. See
	// security audit gate-B2.
	//
	// A non-enforcing fast-path advisory check could go here for early
	// rejection, but the cost (one extra read) is paid by every request
	// while only the rare overload case benefits. Deferred until metrics
	// show it's worth.

	return PolicyDecision{
		Allowed:            true,
		EffectiveMode:      mode,
		EffectiveMinConf:   effectiveMinConf,
		RequireHumanCosign: requireHumanCosign,
	}
}

func isStageAllowed(scope kaprov1alpha2.AgentScope, stage string) bool {
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

func isWithinTimeWindows(windows []kaprov1alpha2.AgentTimeWindow) bool {
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

// sameUTCDay returns true when two times fall on the same UTC calendar day.
// Used by reserveAgentPolicySlot to roll DecisionsToday over at midnight UTC
// without needing a separate timer or controller.
func sameUTCDay(a, b time.Time) bool {
	aU := a.UTC()
	bU := b.UTC()
	return aU.Year() == bU.Year() && aU.Month() == bU.Month() && aU.Day() == bU.Day()
}

// reserveAgentPolicySlot atomically validates rate limits and increments the
// AgentPolicy decision counters in one Status().Update() pass — using
// resourceVersion as the CAS predicate to serialise concurrent decision
// submissions. Returns (allowed, denyReason, error).
//
// Security (gate-B2): the previous flow checked counters in
// enforceAgentPolicy (read-only) and incremented them in a separate
// updateAgentPolicyStatus call AFTER the decision was recorded. N parallel
// requests all observed the same stale counter, all passed the limit check,
// and the rate limit was advisory only. This function moves the check and
// the increment into the same optimistic-concurrency-protected write so a
// genuine cap is enforced.
//
// Day rollover (review fix gate-v6.1): DecisionsToday is reset to 0 when
// the recorded LastDecisionAt falls on a prior UTC day. This is computed
// inside the CAS loop so concurrent first-request-of-day racers all observe
// the reset coherently.
//
// Retries on conflict (typical at high contention); gives up after maxRetries
// and returns the conflict so the caller can fail the request rather than
// silently overshooting the limit.
func (s *Server) reserveAgentPolicySlot(ctx context.Context, policy *kaprov1alpha2.Policy) (bool, string, error) {
	const maxRetries = 5
	key := client.ObjectKey{Namespace: policy.Namespace, Name: policy.Name}
	now := time.Now().UTC()

	var lastConflict error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Refetch on retry to pick up the latest resourceVersion + counters.
		if attempt > 0 {
			var fresh kaprov1alpha2.Policy
			if err := s.Client.Get(ctx, key, &fresh); err != nil {
				return false, "", fmt.Errorf("refetch AgentPolicy %s for retry: %w", policy.Name, err)
			}
			*policy = fresh
		}

		// Day-rollover reset (review fix gate-v6.1): if LastDecisionAt is
		// from a prior UTC day, zero DecisionsToday before rate-limit
		// validation. Without this, MaxApprovalsPerDay becomes a lifetime
		// cap once enough requests accumulate.
		if policy.Status.LastDecisionAt != "" {
			if last, parseErr := time.Parse(time.RFC3339, policy.Status.LastDecisionAt); parseErr == nil {
				if !sameUTCDay(last, now) {
					policy.Status.DecisionsToday = 0
				}
			}
		}

		// Validate against rate limits using the freshly-observed (and
		// possibly day-reset) counters.
		if policy.Spec.RateLimits != nil {
			rl := policy.Spec.RateLimits
			if rl.MaxApprovalsPerDay > 0 && policy.Status.DecisionsToday >= rl.MaxApprovalsPerDay {
				return false, "daily approval limit reached", nil
			}
			if rl.MaxConcurrent > 0 && policy.Status.ActiveDecisions >= rl.MaxConcurrent {
				return false, "concurrent approval limit reached", nil
			}
			if rl.Cooldown != "" && policy.Status.LastDecisionAt != "" {
				cooldown, err := time.ParseDuration(rl.Cooldown)
				if err == nil {
					last, parseErr := time.Parse(time.RFC3339, policy.Status.LastDecisionAt)
					if parseErr == nil && now.Sub(last) < cooldown {
						return false, fmt.Sprintf("cooldown period not elapsed (last: %s)", policy.Status.LastDecisionAt), nil
					}
				}
			}
		}

		// Increment + write in one CAS pass.
		policy.Status.ActiveDecisions++
		policy.Status.DecisionsToday++
		policy.Status.LastDecisionAt = now.Format(time.RFC3339)
		policy.Status.ObservedGeneration = policy.Generation

		if err := s.Client.Status().Update(ctx, policy); err != nil {
			if apierrors.IsConflict(err) {
				// Roll back the local mutation; refetch+retry on next loop.
				policy.Status.ActiveDecisions--
				policy.Status.DecisionsToday--
				lastConflict = err
				continue
			}
			return false, "", fmt.Errorf("update AgentPolicy status: %w", err)
		}
		return true, "", nil
	}
	return false, "", fmt.Errorf("AgentPolicy %s rate-limit reservation lost %d races: %w", policy.Name, maxRetries, lastConflict)
}

// releaseAgentPolicySlot decrements ActiveDecisions when an in-flight
// reservation leaves the active Decision API write path. Called by the
// deferred-release path in handleDecide on any non-2xx exit after
// reserveAgentPolicySlot succeeded, and after successful durable decision
// recording.
//
// DecisionsToday is intentionally NOT decremented — it's a daily quota
// counted against the request RATE, not in-flight load. If the decision
// failed at the post-reserve gate (SAR, status patch, Approval create),
// the agent still tried; counting it against the daily budget is
// defensible and avoids unbounded retry loops. Day rollover in
// reserveAgentPolicySlot zeroes it at midnight UTC anyway.
//
// Floor at zero so a buggy double-release can't drive the counter negative
// (apiserver would accept a negative int32 but consumers would be confused).
// Retries on conflict up to maxRetries; non-conflict errors surface so the
// caller can log them — leaking one slot is acceptable, swallowing the
// underlying error is not.
func (s *Server) releaseAgentPolicySlot(ctx context.Context, policy *kaprov1alpha2.Policy) error {
	const maxRetries = 5
	key := client.ObjectKey{Namespace: policy.Namespace, Name: policy.Name}

	for attempt := 0; attempt < maxRetries; attempt++ {
		var fresh kaprov1alpha2.Policy
		if err := s.Client.Get(ctx, key, &fresh); err != nil {
			return fmt.Errorf("refetch AgentPolicy %s for release: %w", policy.Name, err)
		}
		if fresh.Status.ActiveDecisions <= 0 {
			// Nothing to release; treat as success rather than driving
			// the counter negative.
			return nil
		}
		fresh.Status.ActiveDecisions--
		if err := s.Client.Status().Update(ctx, &fresh); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return fmt.Errorf("update AgentPolicy %s for release: %w", policy.Name, err)
		}
		return nil
	}
	return fmt.Errorf("AgentPolicy %s slot release lost %d races", policy.Name, maxRetries)
}
