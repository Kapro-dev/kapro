package controller

// env_fsm.go implements the per-target gate FSM that previously lived in
// SyncReconciler. After the Sync CRD fold all execution happens inline inside
// ReleaseReconciler: instead of standalone Sync objects we advance entries in
// release.Status.Targets.
//
// Calling convention:
//   - Every method takes (ctx, release, i) where i is the index into
//     release.Status.Targets that is being advanced.
//   - Mutations are made in-place; the caller persists them with a single
//     Status().Patch at the end of handleProgressing.
//   - transitionTargetTo is the only safe writer of target.Phase — it also sets
//     PhaseEnteredAt, StartedAt, and fires notifications.

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/webhook/token"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
)

// notificationPolicyFrom converts a *GatePolicySpec into the value type expected
// by the notification package. Returns notification.EmptyPolicy when policy is nil
// or has no channels configured.
func notificationPolicyFrom(policy *kaprov1alpha1.GatePolicySpec) notification.NotificationPolicy {
	if policy == nil || len(policy.Notifications) == 0 {
		return notification.EmptyPolicy
	}
	channels := make([]notification.Channel, 0, len(policy.Notifications))
	for _, spec := range policy.Notifications {
		ch := notification.Channel{
			Type:   spec.Type,
			Target: spec.Channel,
		}
		if spec.URL != "" {
			ch.Target = spec.URL
		}
		if spec.Email != nil {
			ch.Email = &notification.EmailConfig{
				To:            spec.Email.To,
				From:          spec.Email.From,
				SMTPSecretRef: spec.Email.SmtpSecretRef.Name,
			}
		}
		channels = append(channels, ch)
	}
	return notification.NotificationPolicy{Channels: channels}
}

// targetToGateContext builds the gate evaluation context from an inline target entry.
func targetToGateContext(release *kaprov1alpha1.Release, target *kaprov1alpha1.TargetStatus) *gate.Context {
	return &gate.Context{
		Name:       syncName(release.Name, target.PipelineRef, target.Stage, target.Target),
		Namespace:  release.Namespace,
		ReleaseRef: release.Name,
		Target:     target.Target,
		Pipeline:   target.Pipeline,
		Stage:      target.Stage,
		Version:    target.Version,
		StartedAt:  target.StartedAt,
	}
}

// targetAppKey returns the MemberCluster.status.currentVersions key for this target.
func targetAppKey(target *kaprov1alpha1.TargetStatus) string {
	if target.AppKey != "" {
		return target.AppKey
	}
	return target.ReleaseRef
}

// resolveSyncArgs builds the final args map for a GateTemplate: template
// defaults are overridden by runtime sync context values.
func resolveSyncArgs(tmpl *kaprov1alpha1.GateTemplateSpec, ctx *gate.Context) map[string]string {
	args := make(map[string]string)
	for _, a := range tmpl.Args {
		if a.Value != "" {
			args[a.Name] = a.Value
		}
	}
	if ctx != nil {
		args["version"] = ctx.Version
		args["target"] = ctx.Target
		args["release"] = ctx.ReleaseRef
		args["pipeline"] = ctx.Pipeline
		args["stage"] = ctx.Stage
	}
	return args
}

// advanceAllTargets runs one FSM step for every non-terminal target.
// Mutations are applied in-place; the caller patches release status once.
func (r *ReleaseReconciler) advanceAllTargets(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	result := ctrl.Result{RequeueAfter: requeueNormal}

	for i := range release.Status.Targets {
		switch release.Status.Targets[i].Phase {
		case kaprov1alpha1.SyncPhaseConverged, kaprov1alpha1.SyncPhaseFailed:
			continue
		}
		targetResult, err := r.advanceTarget(ctx, release, i)
		if err != nil {
			return ctrl.Result{}, err
		}
		// Keep the most urgent requeue request.
		if targetResult.Requeue {
			result = targetResult
		} else if targetResult.RequeueAfter > 0 {
			if result.RequeueAfter == 0 || targetResult.RequeueAfter < result.RequeueAfter {
				result = targetResult
			}
		}
	}

	return result, nil
}

// advanceTarget dispatches one FSM step for release.Status.Targets[i].
func (r *ReleaseReconciler) advanceTarget(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]
	switch target.Phase {
	case "":
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhasePending)
		return ctrl.Result{Requeue: true}, nil
	case kaprov1alpha1.SyncPhasePending:
		return r.handleEnvPending(ctx, release, i)
	case kaprov1alpha1.SyncPhaseVerification:
		return r.handleEnvVerification(ctx, release, i)
	case kaprov1alpha1.SyncPhaseHealthCheck:
		return r.handleEnvHealthCheck(ctx, release, i)
	case kaprov1alpha1.SyncPhaseSoaking:
		return r.handleEnvSoaking(ctx, release, i)
	case kaprov1alpha1.SyncPhaseMetricsCheck:
		return r.handleEnvMetricsCheck(ctx, release, i)
	case kaprov1alpha1.SyncPhaseWaitingApproval:
		return r.handleEnvWaitingApproval(ctx, release, i)
	case kaprov1alpha1.SyncPhaseApplying:
		return r.handleEnvApplying(ctx, release, i)
	}
	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) handleEnvPending(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]

	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err != nil {
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}

	if !isHeartbeatFresh(mc.Status.LastHeartbeat) {
		log.FromContext(ctx).Info("cluster heartbeat stale, waiting", "cluster", target.Target)
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseVerification)
	return ctrl.Result{Requeue: true}, nil
}

func (r *ReleaseReconciler) handleEnvVerification(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]
	g := r.VerificationGate
	if g == nil {
		log.FromContext(ctx).Info("VerificationGate is nil — skipping", "target", target.Target)
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseHealthCheck)
		return ctrl.Result{Requeue: true}, nil
	}

	gateCtx := targetToGateContext(release, target)
	result, err := g.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: target.Gate})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("verification gate: %w", err)
	}

	if result.IsPassed() {
		r.Recorder.Eventf(release, corev1.EventTypeNormal, "GatePassed", "VerificationGate passed for %s", target.Target)
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseHealthCheck)
		return ctrl.Result{Requeue: true}, nil
	}

	r.Recorder.Eventf(release, corev1.EventTypeWarning, "GateFailed", "VerificationGate: %s", result.Message)
	return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
}

func (r *ReleaseReconciler) handleEnvHealthCheck(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]
	log := log.FromContext(ctx)

	// CRD path (default): read health directly from mc.Status.Health which is
	// written by the cluster-controller using the spoke's own Flux/Argo status.
	// This avoids the hub's in-cluster client ever touching spoke workloads.
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	h := mc.Status.Health
	log.Info("health check (CRD path)", "allReady", h.AllWorkloadsReady,
		"ready", h.ReadyWorkloads, "total", h.TotalWorkloads, "target", target.Target)

	switch {
	case h.AllWorkloadsReady:
		// Spoke explicitly reported all-clear — advance.
		return r.transitionEnvToSoakOrMetrics(ctx, release, i)
	case h.FailedWorkloads > 0:
		r.failEnv(ctx, release, i, target.Gate,
			fmt.Sprintf("health check: %d/%d workloads failed: %s",
				h.FailedWorkloads, h.TotalWorkloads, h.Message))
		return ctrl.Result{}, nil
	default:
		// Either spoke hasn't reported yet or workloads are still progressing.
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}
}

func (r *ReleaseReconciler) transitionEnvToSoakOrMetrics(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]
	if target.Gate == nil || target.Gate.Gate.SoakTime == "" {
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}
	r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseSoaking)
	return ctrl.Result{Requeue: true}, nil
}

func (r *ReleaseReconciler) handleEnvSoaking(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]
	if target.Gate == nil {
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}

	soakGate := r.SoakGate
	if soakGate == nil {
		return ctrl.Result{}, fmt.Errorf("soak gate not configured")
	}

	gateCtx := targetToGateContext(release, target)
	result, err := soakGate.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: target.Gate})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("soak gate: %w", err)
	}

	log.FromContext(ctx).Info("soak gate", "phase", result.Phase, "target", target.Target)

	if result.IsPassed() {
		r.Recorder.Eventf(release, corev1.EventTypeNormal, "GatePassed", "SoakGate passed for %s", target.Target)
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
}

func (r *ReleaseReconciler) handleEnvMetricsCheck(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]
	policy := target.Gate

	if policy == nil {
		return r.nextAfterEnvMetrics(ctx, release, i, policy)
	}
	if len(policy.Gate.Metrics) == 0 && len(policy.Gate.Templates) == 0 {
		return r.nextAfterEnvMetrics(ctx, release, i, policy)
	}

	gateCtx := targetToGateContext(release, target)
	gatePassed := true

	if len(policy.Gate.Metrics) > 0 {
		metricsGate := r.MetricsGate
		if metricsGate == nil {
			return ctrl.Result{}, fmt.Errorf("metrics gate not configured")
		}
		for idx, metric := range policy.Gate.Metrics {
			result, err := metricsGate.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: policy, MetricIndex: idx})
			if err != nil {
				log.FromContext(ctx).Error(err, "metrics gate error, will retry", "index", idx)
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
			}
			log.FromContext(ctx).Info("metrics gate", "index", idx, "provider", metric.Provider, "phase", result.Phase)
			if !result.IsPassed() {
				r.Recorder.Eventf(release, corev1.EventTypeWarning, "GateFailed", "MetricsGate[%d]: %s", idx, result.Message)
				gatePassed = false
				if timedOut, failMsg := r.metricsEnvGateTimedOut(target, policy); timedOut {
					r.failEnv(ctx, release, i, policy, failMsg)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
			}
		}
	}

	if len(policy.Gate.Templates) > 0 {
		allPassed, requeueAfter, err := r.evaluateEnvGateTemplates(ctx, release, i, gateCtx, policy)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("evaluateEnvGateTemplates: %w", err)
		}
		if !allPassed {
			gatePassed = false
			if timedOut, failMsg := r.metricsEnvGateTimedOut(target, policy); timedOut {
				r.failEnv(ctx, release, i, policy, failMsg)
				return ctrl.Result{}, nil
			}
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}

	_ = gatePassed
	return r.nextAfterEnvMetrics(ctx, release, i, policy)
}

func (r *ReleaseReconciler) metricsEnvGateTimedOut(target *kaprov1alpha1.TargetStatus, policy *kaprov1alpha1.GatePolicySpec) (bool, string) {
	if policy.Gate.GateTimeout == "" || target.PhaseEnteredAt == "" {
		return false, ""
	}
	timeout, err := time.ParseDuration(policy.Gate.GateTimeout)
	if err != nil {
		return false, ""
	}
	enteredAt, err := time.Parse(time.RFC3339, target.PhaseEnteredAt)
	if err != nil {
		return false, ""
	}
	if time.Since(enteredAt) < timeout {
		return false, ""
	}
	return true, fmt.Sprintf("metrics gate timed out after %s (onFailure=%s)", policy.Gate.GateTimeout, policy.OnFailure)
}

// evaluateEnvGateTemplates evaluates all inline gate templates for the target.
// Results are written into release.Status.Targets[i].Gates in-place.
// No intermediate status patch — caller patches once after all advances.
func (r *ReleaseReconciler) evaluateEnvGateTemplates(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	i int,
	gateCtx *gate.Context,
	policy *kaprov1alpha1.GatePolicySpec,
) (bool, time.Duration, error) {
	log := log.FromContext(ctx)
	target := &release.Status.Targets[i]

	now := time.Now().UTC().Format(time.RFC3339)
	gates := target.Gates
	if gates == nil {
		gates = make([]kaprov1alpha1.GateRunStatus, 0, len(policy.Gate.Templates))
	}

	allPassed := true
	requeueAfter := requeueNormal

	for j := range policy.Gate.Templates {
		tmpl := &policy.Gate.Templates[j]
		gateName := tmpl.Name
		if gateName == "" {
			gateName = fmt.Sprintf("gate-%d", j)
		}

		gateStatus := findOrCreateGateStatus(gates, gateName, now)

		if gateStatus.Phase == kaprov1alpha1.GatePhasePassed {
			continue
		}
		if gateStatus.Phase == kaprov1alpha1.GatePhaseFailed {
			allPassed = false
			continue
		}

		args := resolveSyncArgs(tmpl, gateCtx)
		g, err := r.gateForTemplate(tmpl)
		if err != nil {
			return false, 0, fmt.Errorf("gate for template %q: %w", gateName, err)
		}

		result, err := g.Evaluate(ctx, gate.Request{
			Context:  gateCtx,
			Template: tmpl,
			Args:     args,
		})
		if err != nil {
			log.Error(err, "gate template evaluation error, will retry", "gate", gateName)
			gateStatus.Phase = kaprov1alpha1.GatePhaseRunning
			gateStatus.Message = err.Error()
			gateStatus.Attempts++
			setGateStatus(&gates, gateStatus)
			kaprometrics.GateEvaluations.WithLabelValues(tmpl.Type, "error").Inc()
			allPassed = false
			continue
		}

		phase := result.Phase
		if phase == "" {
			phase = kaprov1alpha1.GatePhaseInconclusive
		}
		kaprometrics.GateEvaluations.WithLabelValues(tmpl.Type, strings.ToLower(string(phase))).Inc()

		gateStatus.Phase = phase
		gateStatus.Message = result.Message
		gateStatus.Attempts++
		gateStatus.VendorRef = result.VendorRef
		if len(result.Results) > 0 {
			gateStatus.Results = toAPIConditionResults(result.Results)
		}
		if phase != "" && phase != kaprov1alpha1.GatePhaseRunning && phase != kaprov1alpha1.GatePhasePending {
			gateStatus.FinishedAt = now
		}
		setGateStatus(&gates, gateStatus)

		log.Info("gate template evaluated", "gate", gateName, "phase", phase, "target", target.Target)
		r.Recorder.Eventf(release, corev1.EventTypeNormal, "GateEvaluated",
			"gate %s for %s: %s — %s", gateName, target.Target, phase, result.Message)

		switch phase {
		case kaprov1alpha1.GatePhaseFailed:
			allPassed = false
			if tmpl.FailurePolicy == "skip" {
				gateStatus.Phase = kaprov1alpha1.GatePhasePassed
				gateStatus.Message = "skipped (failurePolicy=skip)"
				setGateStatus(&gates, gateStatus)
			}
		case kaprov1alpha1.GatePhaseInconclusive:
			allPassed = false
			if tmpl.InconclusivePolicy == "halt" {
				gateStatus.Phase = kaprov1alpha1.GatePhaseFailed
				setGateStatus(&gates, gateStatus)
			}
		case kaprov1alpha1.GatePhaseRunning, kaprov1alpha1.GatePhasePending:
			allPassed = false
			if d := parseDurationOrDefault(result.RetryAfter); d < requeueAfter || requeueAfter == requeueNormal {
				requeueAfter = d
			}
		}
	}

	// Persist gate status in the target struct (in-memory; caller patches).
	target.Gates = gates

	return allPassed, requeueAfter, nil
}

// gateForTemplate resolves a Gate implementation from the registry.
// GateRegistry must be non-nil; missing registrations produce a descriptive
// error — never a silent fallback.
func (r *ReleaseReconciler) gateForTemplate(tmpl *kaprov1alpha1.GateTemplateSpec) (gate.Gate, error) {
	if r.GateRegistry == nil {
		return nil, fmt.Errorf("GateRegistry not configured: cannot resolve gate type %q", tmpl.Type)
	}
	g, err := r.GateRegistry.Resolve(tmpl.Type)
	if err != nil {
		return nil, fmt.Errorf("gate type %q not registered: %w", tmpl.Type, err)
	}
	return g, nil
}

func (r *ReleaseReconciler) nextAfterEnvMetrics(ctx context.Context, release *kaprov1alpha1.Release, i int, policy *kaprov1alpha1.GatePolicySpec) (ctrl.Result, error) {
	if policy != nil && policy.Approval != nil && policy.Approval.Required {
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseWaitingApproval)
		return ctrl.Result{Requeue: true}, nil
	}
	r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseApplying)
	return ctrl.Result{Requeue: true}, nil
}

func (r *ReleaseReconciler) handleEnvWaitingApproval(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	target := &release.Status.Targets[i]

	// A rejection set by the webhook server.
	if target.Rejected {
		rejectedBy := target.RejectedBy
		if rejectedBy == "" {
			rejectedBy = "unknown"
		}
		r.failEnv(ctx, release, i, target.Gate, fmt.Sprintf("rejected by %s", rejectedBy))
		return ctrl.Result{}, nil
	}

	// Send the approval notification exactly once per WaitingApproval entry.
	if target.ApprovalSentAt == "" {
		r.sendEnvApprovalNotification(ctx, release, i)
	}

	approvalGate := r.ApprovalGate
	if approvalGate == nil {
		return ctrl.Result{}, fmt.Errorf("approval gate not configured")
	}

	gateCtx := targetToGateContext(release, target)
	result, err := approvalGate.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: target.Gate})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("approval gate: %w", err)
	}

	log.FromContext(ctx).Info("approval gate", "phase", result.Phase, "target", target.Target)

	if result.IsPassed() {
		r.transitionEnvTo(ctx, release, i, kaprov1alpha1.SyncPhaseApplying)
		return ctrl.Result{Requeue: true}, nil
	}

	r.Recorder.Eventf(release, corev1.EventTypeNormal, "WaitingApproval",
		"Waiting for Approval CR for %s", target.Target)
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

// sendEnvApprovalNotification dispatches the approval notification and records
// ApprovalSentAt in-memory. Errors are logged and never block the reconcile.
func (r *ReleaseReconciler) sendEnvApprovalNotification(ctx context.Context, release *kaprov1alpha1.Release, i int) {
	logger := log.FromContext(ctx)
	target := &release.Status.Targets[i]

	var approveURL, rejectURL string
	if len(r.ApprovalSecret) > 0 && r.ExternalURL != "" {
		var err error
		approveURL, rejectURL, err = r.buildTargetApprovalURLs(release, target)
		if err != nil {
			logger.Error(err, "failed to build approval URLs — notification will omit links")
		}
	}

	if r.Notifier != nil {
		r.Notifier.Notify(ctx, notification.Event{
			Phase:      string(kaprov1alpha1.SyncPhaseWaitingApproval),
			Version:    target.Version,
			Target:     target.Target,
			Release:    release.Name,
			Message:    "Approval required to proceed",
			ApproveURL: approveURL,
			RejectURL:  rejectURL,
		}, notificationPolicyFrom(target.Gate))
	}

	// Record in-memory so we don't re-notify on next requeue.
	// The caller's status patch will persist this.
	target.ApprovalSentAt = time.Now().UTC().Format(time.RFC3339)
}

// buildTargetApprovalURLs returns signed approve and reject URLs for the given target.
// Token UID is string(release.UID) + "/" + targetKey to prevent replay across
// name reuse while remaining verifiable without looking up a Sync object.
func (r *ReleaseReconciler) buildTargetApprovalURLs(release *kaprov1alpha1.Release, target *kaprov1alpha1.TargetStatus) (approveURL, rejectURL string, err error) {
	exp := time.Now().Add(token.DefaultTTL).Unix()
	targetKey := syncName(release.Name, target.PipelineRef, target.Stage, target.Target)

	baseClaims := token.Claims{
		SyncName:  targetKey,
		Namespace: release.Namespace,
		Release:   release.Name,
		Target:    target.Target,
		Version:   target.Version,
		UID:       string(release.UID) + "/" + targetKey,
		Exp:       exp,
	}

	approveClaims := baseClaims
	approveClaims.Action = "approve"
	approveToken, err := token.Sign(approveClaims, r.ApprovalSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign approve token: %w", err)
	}

	rejectClaims := baseClaims
	rejectClaims.Action = "reject"
	rejectToken, err := token.Sign(rejectClaims, r.ApprovalSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign reject token: %w", err)
	}

	base := strings.TrimRight(r.ExternalURL, "/")
	approveURL = fmt.Sprintf("%s/approve/%s?token=%s", base, targetKey, approveToken)
	rejectURL = fmt.Sprintf("%s/reject/%s?token=%s", base, targetKey, rejectToken)
	return approveURL, rejectURL, nil
}

func (r *ReleaseReconciler) handleEnvApplying(ctx context.Context, release *kaprov1alpha1.Release, i int) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	target := &release.Status.Targets[i]

	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("membercluster %s not found: %w", target.Target, err)
	}

	// Capture current version for rollback before we change anything.
	if target.PreviousVersion == "" {
		if current := mc.Status.CurrentVersions[targetAppKey(target)]; current != "" {
			target.PreviousVersion = current
		}
	}

	// Resolve actuator from MemberCluster spec and issue Apply exactly once.
	// target.ApplyIssued is reset to false by transitionEnvTo when entering Applying,
	// so each new delivery cycle calls Apply() a single time regardless of how many
	// times the reconciler is re-triggered during convergence polling.
	if r.ActuatorRegistry != nil && !target.ApplyIssued {
		act, err := r.ActuatorRegistry.Resolve(mc.Spec.Actuator.Type)
		if err != nil {
			log.Error(err, "failed to resolve actuator")
			r.Recorder.Eventf(release, corev1.EventTypeWarning, "ActuatorResolveFailed", "%s: %s", target.Target, err.Error())
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		if err := act.Apply(ctx, actuator.ApplyRequest{
			Cluster:         &mc,
			Version:         target.Version,
			PreviousVersion: target.PreviousVersion,
			AppKey:          targetAppKey(target),
		}); err != nil {
			log.Error(err, "Actuator.Apply failed, will retry")
			r.Recorder.Eventf(release, corev1.EventTypeWarning, "ApplyFailed", "%s: %s", target.Target, err.Error())
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		target.ApplyIssued = true
		log.Info("Actuator.Apply issued — polling for convergence",
			"cluster", target.Target,
			"actuator", mc.Spec.Actuator.Type,
			"version", target.Version,
		)
	}

	// Poll MemberCluster for convergence.
	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		mc.Status.CurrentVersions[targetAppKey(target)] == target.Version {
		log.Info("cluster converged", "cluster", target.Target, "version", target.Version)
		r.Recorder.Eventf(release, corev1.EventTypeNormal, "Applied",
			"Version %s applied to %s", target.Version, target.Target)
		target.Phase = kaprov1alpha1.SyncPhaseConverged
		target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		kaprometrics.SyncTransitions.WithLabelValues(string(kaprov1alpha1.SyncPhaseConverged), "success").Inc()
		return ctrl.Result{}, nil
	}

	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
		r.failEnv(ctx, release, i, target.Gate,
			fmt.Sprintf("cluster %s reported Failed phase", target.Target))
		return ctrl.Result{}, nil
	}

	log.Info("waiting for convergence",
		"cluster", target.Target,
		"clusterPhase", mc.Status.Phase,
		"currentVersion", mc.Status.CurrentVersions[targetAppKey(target)],
		"wantVersion", target.Version,
	)
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

// transitionEnvTo mutates target.Phase and related timestamps in-place.
// Caller patches release status once after all advances.
func (r *ReleaseReconciler) transitionEnvTo(ctx context.Context, release *kaprov1alpha1.Release, i int, phase kaprov1alpha1.SyncPhase) {
	target := &release.Status.Targets[i]
	target.Phase = phase
	if phase == kaprov1alpha1.SyncPhaseSoaking && target.StartedAt == "" {
		target.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// Reset the apply-once guard so each new Applying cycle issues exactly one Apply().
	if phase == kaprov1alpha1.SyncPhaseApplying {
		target.ApplyIssued = false
	}
	target.PhaseEnteredAt = time.Now().UTC().Format(time.RFC3339)

	r.Recorder.Eventf(release, corev1.EventTypeNormal, "PhaseTransition",
		"target %s/%s/%s → %s", target.PipelineRef, target.Stage, target.Target, phase)

	result := "success"
	if phase == kaprov1alpha1.SyncPhaseFailed {
		result = "failed"
	}
	kaprometrics.SyncTransitions.WithLabelValues(string(phase), result).Inc()

	if r.Notifier != nil && phase != kaprov1alpha1.SyncPhaseWaitingApproval {
		r.Notifier.Notify(ctx, notification.Event{
			Phase:     string(phase),
			Version:   target.Version,
			Target:    target.Target,
			Release:   release.Name,
			IsFailure: phase == kaprov1alpha1.SyncPhaseFailed,
		}, notificationPolicyFrom(target.Gate))
	}
}

// failEnv sets target.Phase=Failed and records the failure message in-place.
// If onFailure==rollback and a previous version exists, triggerEnvRollback is also called.
func (r *ReleaseReconciler) failEnv(ctx context.Context, release *kaprov1alpha1.Release, i int, policy *kaprov1alpha1.GatePolicySpec, msg string) {
	target := &release.Status.Targets[i]
	target.Phase = kaprov1alpha1.SyncPhaseFailed
	target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	target.Message = msg

	r.Recorder.Eventf(release, corev1.EventTypeWarning, "TargetFailed",
		"target %s/%s/%s failed: %s", target.PipelineRef, target.Stage, target.Target, msg)
	kaprometrics.SyncTransitions.WithLabelValues(string(kaprov1alpha1.SyncPhaseFailed), "failed").Inc()

	if r.Notifier != nil {
		r.Notifier.Notify(ctx, notification.Event{
			Phase:     string(kaprov1alpha1.SyncPhaseFailed),
			Version:   target.Version,
			Target:    target.Target,
			Release:   release.Name,
			Message:   msg,
			IsFailure: true,
		}, notificationPolicyFrom(policy))
	}

	onFailure := "halt"
	if policy != nil && policy.OnFailure != "" {
		onFailure = policy.OnFailure
	}

	if onFailure == "rollback" && target.PreviousVersion != "" {
		r.triggerEnvRollback(ctx, release, i)
	}
}

// triggerEnvRollback calls Actuator.Rollback() immediately, then appends a new
// TargetStatus entry targeting the previous version.
func (r *ReleaseReconciler) triggerEnvRollback(ctx context.Context, release *kaprov1alpha1.Release, i int) {
	log := log.FromContext(ctx)
	target := &release.Status.Targets[i]

	// 1. Immediately call actuator.Rollback() so the delivery system starts
	//    reverting without waiting for the rollback target entry to reconcile.
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err == nil {
		if r.ActuatorRegistry != nil {
			if act, actErr := r.ActuatorRegistry.Resolve(mc.Spec.Actuator.Type); actErr == nil {
				if rbErr := act.Rollback(ctx, &mc, target.PreviousVersion); rbErr != nil {
					log.Error(rbErr, "actuator Rollback() failed — rollback target will re-apply it")
				} else {
					log.Info("actuator Rollback() succeeded",
						"cluster", target.Target,
						"previousVersion", target.PreviousVersion,
					)
				}
			}
		}
	}

	// 2. Append rollback entry (idempotent: skip if one already exists).
	for _, t := range release.Status.Targets {
		if t.Rollback && t.Target == target.Target &&
			t.PipelineRef == target.PipelineRef && t.Stage == target.Stage {
			log.Info("rollback target entry already exists", "target", target.Target)
			return
		}
	}

	rollbackTarget := kaprov1alpha1.TargetStatus{
		ReleaseRef:  release.Name,
		Target:      target.Target,
		PipelineRef: target.PipelineRef,
		Pipeline:    target.Pipeline,
		Stage:       target.Stage,
		Version:     target.PreviousVersion,
		Gate:        target.Gate,
		AppKey:      target.AppKey,
		Phase:       kaprov1alpha1.SyncPhasePending,
		Rollback:    true,
	}
	release.Status.Targets = append(release.Status.Targets, rollbackTarget)

	log.Info("appended rollback target entry",
		"target", target.Target,
		"targetVersion", target.PreviousVersion,
	)
	r.Recorder.Eventf(release, corev1.EventTypeWarning, "RollbackTriggered",
		"Auto-rollback to %s triggered for %s", target.PreviousVersion, target.Target)
}

// approvalForRelease maps an Approval to the Release it should unblock.
// Approval.spec.release is the Release name; Approval.namespace is the namespace.
func approvalForRelease(ctx context.Context, obj client.Object) []ctrl.Request {
	approval, ok := obj.(*kaprov1alpha1.Approval)
	if !ok {
		return nil
	}
	if approval.Spec.Release == "" {
		return nil
	}
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{
			Name:      approval.Spec.Release,
			Namespace: approval.Namespace,
		},
	}}
}
