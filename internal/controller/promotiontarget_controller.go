package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
)

const maxImmediatePhaseAdvances = 8

// contextKeyPromotionTarget is used to pass the PromotionTarget object through
// context so FSM transition helpers can emit events on the target itself.
type contextKeyPromotionTargetType struct{}

var contextKeyPromotionTarget = contextKeyPromotionTargetType{}

func contextWithPromotionTarget(ctx context.Context, rt *kaprov1alpha1.PromotionTarget) context.Context {
	return context.WithValue(ctx, contextKeyPromotionTarget, rt)
}

func promotionTargetFromContext(ctx context.Context) *kaprov1alpha1.PromotionTarget {
	rt, _ := ctx.Value(contextKeyPromotionTarget).(*kaprov1alpha1.PromotionTarget)
	return rt
}

// PromotionTargetReconciler independently advances one PromotionTarget through the
// per-target rollout FSM. It reads the parent PromotionRun (read-only, for suspend
// and version info) and the referenced FleetCluster, but NEVER loads sibling
// PromotionTargets or writes outside its own PromotionTarget.status and
// metadata.labels["kapro.io/phase"].
//
// The parent PromotionRunReconciler aggregates child statuses via indexed queries —
// it never runs the FSM itself.
type PromotionTargetReconciler struct {
	client.Client
	Recorder           record.EventRecorder
	Scheme             *runtime.Scheme
	ActuatorRegistry   *actuator.Registry
	Notifier           notification.Notifier
	ApprovalSecret     []byte
	ExternalURL        string
	GateRegistry       *gate.Registry
	HeartbeatNamespace string

	// ShardPredicate optionally filters objects by shard label for horizontal scaling.
	// When nil, all objects are processed.
	ShardPredicate predicate.Predicate
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotiontargets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotiontargets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get
// +kubebuilder:rbac:groups=kapro.io,resources=fleetclusters,verbs=get
// +kubebuilder:rbac:groups=kapro.io,resources=fleetclusters/status,verbs=get;patch

func (r *PromotionTargetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	resultLabel := "success"
	defer func() {
		kaprometrics.ControllerReconciles.WithLabelValues("promotion_target", resultLabel).Inc()
		kaprometrics.ControllerReconcileDuration.WithLabelValues("promotion_target").Observe(time.Since(start).Seconds())
	}()

	var rt kaprov1alpha1.PromotionTarget
	if err := r.Get(ctx, req.NamespacedName, &rt); err != nil {
		resultLabel = "error"
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	prevTarget := rt.Status.TargetStatus

	// Terminal — nothing to do.
	phase := rt.Status.Phase
	switch phase {
	case kaprov1alpha1.TargetPhaseConverged, kaprov1alpha1.TargetPhaseFailed, kaprov1alpha1.TargetPhaseSkipped:
		if updateErr := r.syncPromotionTargetPhaseLabel(ctx, &rt); updateErr != nil {
			kaprometrics.StatusWrites.WithLabelValues("promotiontarget", "error").Inc()
			resultLabel = "error"
			return ctrl.Result{}, fmt.Errorf("patch terminal PromotionTarget phase label %s: %w", rt.Name, updateErr)
		}
		return ctrl.Result{}, nil
	}

	// Read parent PromotionRun — read-only, never mutated here.
	var promotionrun kaprov1alpha1.PromotionRun
	if err := r.Get(ctx, client.ObjectKey{Name: rt.Spec.PromotionRunRef}, &promotionrun); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Honor parent suspend.
	if promotionrun.Spec.Suspended {
		log.FromContext(ctx).Info("parent PromotionRun suspended, skipping", "promotionrun", promotionrun.Name)
		return ctrl.Result{}, nil
	}

	// Honor cancellation signal from parent (spec.cancelled).
	// Parent writes spec (owns it), child transitions status to Failed (owns it).
	if rt.Spec.Cancelled {
		log.FromContext(ctx).Info("target cancelled by parent", "reason", rt.Spec.CancelledReason)
		rt.Status.Phase = kaprov1alpha1.TargetPhaseFailed
		rt.Status.Message = "cancelled: " + rt.Spec.CancelledReason
		rt.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		r.updatePromotionTargetStatusContract(&rt)
		if updateErr := r.Status().Update(ctx, &rt); updateErr != nil {
			return ctrl.Result{}, fmt.Errorf("update cancelled PromotionTarget status: %w", updateErr)
		}
		if updateErr := r.syncPromotionTargetPhaseLabel(ctx, &rt); updateErr != nil {
			kaprometrics.StatusWrites.WithLabelValues("promotiontarget", "error").Inc()
			resultLabel = "error"
			return ctrl.Result{}, fmt.Errorf("patch cancelled PromotionTarget phase label %s: %w", rt.Name, updateErr)
		}
		return ctrl.Result{}, nil
	}

	// Build the in-memory TargetStatus from the PromotionTarget for FSM execution.
	target := targetStatusFromPromotionTarget(&rt)

	// Inject PromotionTarget into context so FSM helpers can emit events on it.
	ctx = contextWithPromotionTarget(ctx, &rt)

	// Run the FSM until it reaches a stable state that actually needs a requeue,
	// external event, or durable persistence boundary.
	result, err := r.advanceTargetUntilStable(ctx, &promotionrun, &target, &rt)
	if err != nil {
		resultLabel = "error"
		return ctrl.Result{}, err
	}

	// Write back to PromotionTarget.status.
	rt.Status.TargetStatus = target
	r.updatePromotionTargetStatusContract(&rt)
	if updateErr := r.Status().Update(ctx, &rt); updateErr != nil {
		kaprometrics.StatusWrites.WithLabelValues("promotiontarget", "error").Inc()
		resultLabel = "error"
		return ctrl.Result{}, fmt.Errorf("update PromotionTarget status %s: %w", rt.Name, updateErr)
	}
	if updateErr := r.syncPromotionTargetPhaseLabel(ctx, &rt); updateErr != nil {
		kaprometrics.StatusWrites.WithLabelValues("promotiontarget", "error").Inc()
		resultLabel = "error"
		return ctrl.Result{}, fmt.Errorf("patch PromotionTarget phase label %s: %w", rt.Name, updateErr)
	}
	kaprometrics.StatusWrites.WithLabelValues("promotiontarget", "success").Inc()
	r.notifyPersistedTransitions(ctx, &promotionrun, &prevTarget, &target)

	return result, nil
}

func (r *PromotionTargetReconciler) advanceTargetUntilStable(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.PromotionTarget) (ctrl.Result, error) {
	for i := 0; i < maxImmediatePhaseAdvances; i++ {
		beforePhase := target.Phase
		result, err := r.advanceTarget(ctx, promotionrun, target, rt)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !isImmediateRequeue(result) {
			return result, nil
		}
		if target.Phase == beforePhase {
			return result, nil
		}
		// Persist the transition into Applying before executing external side effects
		// like activePromotionRun claims and actuator Apply() on the next reconcile.
		if target.Phase == kaprov1alpha1.TargetPhaseApplying {
			return result, nil
		}
	}
	return ctrl.Result{Requeue: true}, nil
}

func isImmediateRequeue(result ctrl.Result) bool {
	return result.Requeue && result.RequeueAfter == 0 //nolint:staticcheck // SA1019: result.Requeue deprecated but replacement needs larger refactor
}

// advanceTarget dispatches one FSM step for the given target.
func (r *PromotionTargetReconciler) advanceTarget(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.PromotionTarget) (ctrl.Result, error) {
	switch target.Phase {
	case "":
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhasePending)
		return ctrl.Result{Requeue: true}, nil
	case kaprov1alpha1.TargetPhasePending:
		return r.handlePending(ctx, promotionrun, target)
	case kaprov1alpha1.TargetPhaseVerification:
		return r.handleVerification(ctx, promotionrun, target, rt)
	case kaprov1alpha1.TargetPhaseHealthCheck:
		return r.handleHealthCheck(ctx, promotionrun, target)
	case kaprov1alpha1.TargetPhaseSoaking:
		return r.handleSoaking(ctx, promotionrun, target, rt)
	case kaprov1alpha1.TargetPhaseMetricsCheck:
		return r.handleMetricsCheck(ctx, promotionrun, target, rt)
	case kaprov1alpha1.TargetPhaseWaitingApproval:
		return r.handleWaitingApproval(ctx, promotionrun, target, rt)
	case kaprov1alpha1.TargetPhaseApplying:
		return r.handleApplying(ctx, promotionrun, target)
	}
	return ctrl.Result{}, nil
}

func (r *PromotionTargetReconciler) handlePending(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) (ctrl.Result, error) {
	var mc kaprov1alpha1.FleetCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		target.MissingMCCount++
		if target.MissingMCCount >= missingMCFailThreshold {
			r.failTarget(ctx, promotionrun, target,
				fmt.Sprintf("FleetCluster %q not found after %d attempts", target.Target, target.MissingMCCount))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}
	target.MissingMCCount = 0
	if result, ok, err := r.requireFreshHeartbeat(ctx, promotionrun, target, &mc); err != nil || !ok {
		return result, err
	}

	// FleetCluster exists and is reachable — advance to verification.
	r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseVerification)
	return ctrl.Result{Requeue: true}, nil
}

func (r *PromotionTargetReconciler) handleVerification(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.PromotionTarget) (ctrl.Result, error) {
	g, err := r.GateRegistry.Resolve("verification")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("verification gate: %w", err)
	}

	gateCtx := targetToGateContext(promotionrun, target, rt)
	result, err := g.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: target.Gate})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("verification gate: %w", err)
	}

	if result.IsPassed() {
		r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "GatePassed", "[%s/%s] artifact signature verified", target.Stage, target.Target)
		if rt := promotionTargetFromContext(ctx); rt != nil {
			r.Recorder.Event(rt, corev1.EventTypeNormal, "VerificationPassed", "artifact signature verified")
		}
		r.notifyGateEvent(ctx, promotionrun, target, notification.EventGatePassed, "verification", "artifact signature verified", false)
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseHealthCheck)
		return ctrl.Result{Requeue: true}, nil
	}
	if result.Phase == kaprov1alpha1.GatePhaseFailed {
		r.notifyGateEvent(ctx, promotionrun, target, notification.EventGateFailed, "verification", result.Message, true)
		r.failTarget(ctx, promotionrun, target, result.Message)
		return ctrl.Result{}, nil
	}

	r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "GateFailed", "[%s/%s] verification: %s", target.Stage, target.Target, result.Message)
	if rt := promotionTargetFromContext(ctx); rt != nil {
		r.Recorder.Eventf(rt, corev1.EventTypeWarning, "VerificationFailed", "verification: %s", result.Message)
	}
	r.notifyGateEvent(ctx, promotionrun, target, notification.EventGateFailed, "verification", result.Message, true)
	return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
}

func (r *PromotionTargetReconciler) handleHealthCheck(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var mc kaprov1alpha1.FleetCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		target.MissingMCCount++
		if target.MissingMCCount >= missingMCFailThreshold {
			r.failTarget(ctx, promotionrun, target,
				fmt.Sprintf("FleetCluster %q not found after %d attempts", target.Target, target.MissingMCCount))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}
	target.MissingMCCount = 0
	if result, ok, err := r.requireFreshHeartbeat(ctx, promotionrun, target, &mc); err != nil || !ok {
		return result, err
	}

	h := mc.Status.Health
	l.Info("health check (CRD path)", "allReady", h.AllWorkloadsReady,
		"ready", h.ReadyWorkloads, "total", h.TotalWorkloads, "target", target.Target)

	switch {
	case h.AllWorkloadsReady:
		return r.transitionToSoakOrMetrics(ctx, promotionrun, target)
	case h.FailedWorkloads > 0:
		r.failTarget(ctx, promotionrun, target,
			fmt.Sprintf("health check: %d/%d workloads failed: %s",
				h.FailedWorkloads, h.TotalWorkloads, h.Message))
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}
}

func (r *PromotionTargetReconciler) transitionToSoakOrMetrics(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) (ctrl.Result, error) {
	if target.Gate == nil || target.Gate.Gate.SoakTime == "" {
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}
	r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseSoaking)
	return ctrl.Result{Requeue: true}, nil
}

func (r *PromotionTargetReconciler) handleSoaking(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.PromotionTarget) (ctrl.Result, error) {
	if target.Gate == nil {
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}

	soakGate, err := r.GateRegistry.Resolve("soak")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("soak gate: %w", err)
	}

	gateCtx := targetToGateContext(promotionrun, target, rt)
	result, err := soakGate.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: target.Gate})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("soak gate: %w", err)
	}

	log.FromContext(ctx).Info("soak gate", "phase", result.Phase, "target", target.Target)

	if result.IsPassed() {
		r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "GatePassed", "[%s/%s] soak timer completed", target.Stage, target.Target)
		if rt := promotionTargetFromContext(ctx); rt != nil {
			r.Recorder.Event(rt, corev1.EventTypeNormal, "SoakPassed", "soak timer completed")
		}
		r.notifyGateEvent(ctx, promotionrun, target, notification.EventGatePassed, "soak", "soak timer completed", false)
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
}

func (r *PromotionTargetReconciler) handleMetricsCheck(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.PromotionTarget) (ctrl.Result, error) {
	policy := target.Gate

	if policy == nil || (len(policy.Gate.Metrics) == 0 && len(policy.Gate.Templates) == 0) {
		return r.nextAfterMetrics(ctx, promotionrun, target)
	}

	gateCtx := targetToGateContext(promotionrun, target, rt)
	now := time.Now().UTC().Format(time.RFC3339)
	gates := target.Gates
	if gates == nil {
		gates = make([]kaprov1alpha1.GateRunStatus, 0, len(policy.Gate.Metrics)+len(policy.Gate.Templates))
	}

	if len(policy.Gate.Metrics) > 0 {
		metricsGate, err := r.GateRegistry.Resolve("metrics")
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("metrics gate: %w", err)
		}
		for idx, metric := range policy.Gate.Metrics {
			gateName := fmt.Sprintf("metrics[%d]", idx)
			gateStatus := findOrCreateGateStatus(gates, gateName, now)
			result, err := metricsGate.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: policy, MetricIndex: idx})
			if err != nil {
				log.FromContext(ctx).Error(err, "metrics gate error, will retry", "index", idx)
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
			}
			phase := result.Phase
			if phase == "" {
				phase = kaprov1alpha1.GatePhaseInconclusive
			}
			gateStatus.Phase = phase
			gateStatus.Message = result.Message
			gateStatus.Attempts++
			gateStatus.VendorRef = result.VendorRef
			gateStatus.Evidence = toAPIGateEvidence(result.Evidence)
			if len(result.Results) > 0 {
				gateStatus.Results = toAPIConditionResults(result.Results)
			}
			if phase != kaprov1alpha1.GatePhaseRunning && phase != kaprov1alpha1.GatePhasePending && phase != kaprov1alpha1.GatePhaseInconclusive {
				gateStatus.FinishedAt = now
			}
			setGateStatus(&gates, gateStatus)
			target.Gates = gates
			log.FromContext(ctx).Info("metrics gate", "index", idx, "provider", metric.Provider, "phase", result.Phase)
			if !result.IsPassed() {
				r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "GateFailed", "[%s/%s] MetricsGate[%d]: %s", target.Stage, target.Target, idx, result.Message)
				if rt := promotionTargetFromContext(ctx); rt != nil {
					r.Recorder.Eventf(rt, corev1.EventTypeWarning, "MetricsFailed", "metrics gate[%d]: %s", idx, result.Message)
				}
				r.notifyGateEvent(ctx, promotionrun, target, notification.EventGateFailed, fmt.Sprintf("metrics[%d]", idx), result.Message, true)
				if timedOut, failMsg := metricsGateTimedOut(target, policy); timedOut {
					r.failTarget(ctx, promotionrun, target, failMsg)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
			}
		}
		r.notifyGateEvent(ctx, promotionrun, target, notification.EventGatePassed, "metrics", "metrics gates passed", false)
	}

	if len(policy.Gate.Templates) > 0 {
		allPassed, requeueAfter, err := r.evaluateGateTemplates(ctx, promotionrun, target, gateCtx, policy)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("evaluateGateTemplates: %w", err)
		}
		if !allPassed {
			if timedOut, failMsg := metricsGateTimedOut(target, policy); timedOut {
				r.failTarget(ctx, promotionrun, target, failMsg)
				return ctrl.Result{}, nil
			}
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}

	return r.nextAfterMetrics(ctx, promotionrun, target)
}

// metricsGateTimedOut checks if the gate has exceeded its timeout.
func metricsGateTimedOut(target *kaprov1alpha1.TargetStatus, policy *kaprov1alpha1.GatePolicySpec) (bool, string) {
	if policy.Gate.GateTimeout == "" || target.PhaseEnteredAt == "" {
		return false, ""
	}
	timeout, err := time.ParseDuration(policy.Gate.GateTimeout)
	if err != nil {
		return true, fmt.Sprintf("invalid gateTimeout %q", policy.Gate.GateTimeout)
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

func (r *PromotionTargetReconciler) evaluateGateTemplates(
	ctx context.Context,
	promotionrun *kaprov1alpha1.PromotionRun,
	target *kaprov1alpha1.TargetStatus,
	gateCtx *gate.Context,
	policy *kaprov1alpha1.GatePolicySpec,
) (bool, time.Duration, error) {
	l := log.FromContext(ctx)
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
		maxAttempts := tmpl.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		if err != nil {
			l.Error(err, "gate template evaluation error, will retry", "gate", gateName)
			gateStatus.Phase = kaprov1alpha1.GatePhaseRunning
			gateStatus.Message = err.Error()
			gateStatus.Attempts++
			if gateStatus.Attempts >= maxAttempts {
				gateStatus.Phase = kaprov1alpha1.GatePhaseFailed
				gateStatus.Message = fmt.Sprintf("gate %s exceeded maxAttempts=%d after evaluation errors: %s", gateName, maxAttempts, err)
				gateStatus.FinishedAt = now
			}
			setGateStatus(&gates, gateStatus)
			allPassed = false
			if gateStatus.Phase == kaprov1alpha1.GatePhaseFailed {
				continue
			}
			continue
		}

		phase := result.Phase
		if phase == "" {
			phase = kaprov1alpha1.GatePhaseInconclusive
		}

		gateStatus.Phase = phase
		gateStatus.Message = result.Message
		gateStatus.Attempts++
		gateStatus.VendorRef = result.VendorRef
		gateStatus.Evidence = toAPIGateEvidence(result.Evidence)
		if len(result.Results) > 0 {
			gateStatus.Results = toAPIConditionResults(result.Results)
		}
		if phase != "" && phase != kaprov1alpha1.GatePhaseRunning && phase != kaprov1alpha1.GatePhasePending {
			gateStatus.FinishedAt = now
		}
		attemptsExhausted := gateStatus.Attempts >= maxAttempts && phase != kaprov1alpha1.GatePhasePassed
		if attemptsExhausted {
			phase = kaprov1alpha1.GatePhaseFailed
			gateStatus.Phase = kaprov1alpha1.GatePhaseFailed
			gateStatus.Message = fmt.Sprintf("gate %s exceeded maxAttempts=%d", gateName, maxAttempts)
			gateStatus.FinishedAt = now
		}
		setGateStatus(&gates, gateStatus)

		l.Info("gate template evaluated", "gate", gateName, "phase", phase, "target", target.Target)
		r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "GateEvaluated",
			"gate %s for %s: %s — %s", gateName, target.Target, phase, result.Message)

		switch phase {
		case kaprov1alpha1.GatePhaseFailed:
			switch tmpl.FailurePolicy {
			case "skip":
				gateStatus.Phase = kaprov1alpha1.GatePhasePassed
				gateStatus.Message = "skipped (failurePolicy=skip)"
				gateStatus.FinishedAt = now
				setGateStatus(&gates, gateStatus)
				continue
			case "retry":
				if !attemptsExhausted {
					gateStatus.Phase = kaprov1alpha1.GatePhaseRunning
					gateStatus.Message = fmt.Sprintf("retrying after failure: %s", result.Message)
					gateStatus.FinishedAt = ""
					setGateStatus(&gates, gateStatus)
					allPassed = false
					if d := parseDurationOrDefault(result.RetryAfter); d < requeueAfter || requeueAfter == requeueNormal {
						requeueAfter = d
					}
					continue
				}
			}
			r.notifyGateEvent(ctx, promotionrun, target, notification.EventGateFailed, gateName, gateStatus.Message, true)
			allPassed = false
		case kaprov1alpha1.GatePhaseInconclusive:
			switch tmpl.InconclusivePolicy {
			case "skip":
				gateStatus.Phase = kaprov1alpha1.GatePhasePassed
				gateStatus.Message = "skipped (inconclusivePolicy=skip)"
				gateStatus.FinishedAt = now
				setGateStatus(&gates, gateStatus)
				continue
			case "halt":
				gateStatus.Phase = kaprov1alpha1.GatePhaseFailed
				gateStatus.FinishedAt = now
				setGateStatus(&gates, gateStatus)
				allPassed = false
				continue
			}
			allPassed = false
			if d := parseDurationOrDefault(result.RetryAfter); d < requeueAfter || requeueAfter == requeueNormal {
				requeueAfter = d
			}
		case kaprov1alpha1.GatePhaseRunning, kaprov1alpha1.GatePhasePending:
			allPassed = false
			if d := parseDurationOrDefault(result.RetryAfter); d < requeueAfter || requeueAfter == requeueNormal {
				requeueAfter = d
			}
		case kaprov1alpha1.GatePhasePassed:
			r.notifyGateEvent(ctx, promotionrun, target, notification.EventGatePassed, gateName, gateStatus.Message, false)
		}
	}

	target.Gates = gates
	return allPassed, requeueAfter, nil
}

func (r *PromotionTargetReconciler) notifyGateEvent(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, eventType, gateName, message string, isFailure bool) {
	if r.Notifier == nil {
		return
	}
	if message == "" {
		message = gateName
	}
	r.Notifier.Notify(ctx, notification.Event{
		Type:          eventType,
		Phase:         string(target.Phase),
		Version:       target.Version,
		Target:        target.Target,
		PromotionRun:  promotionrun.Name,
		PromotionPlan: target.PromotionPlanRef,
		Stage:         target.Stage,
		Message:       fmt.Sprintf("%s: %s", gateName, message),
		IsFailure:     isFailure,
	}, notificationPolicyFrom(target.Gate))
}

func (r *PromotionTargetReconciler) gateForTemplate(tmpl *kaprov1alpha1.GateTemplateSpec) (gate.Gate, error) {
	if r.GateRegistry == nil {
		return nil, fmt.Errorf("GateRegistry not configured: cannot resolve gate type %q", tmpl.Type)
	}
	if tmpl.Type == "plugin" {
		if tmpl.Plugin == nil || tmpl.Plugin.Name == "" {
			return nil, fmt.Errorf("plugin gate requires plugin.name")
		}
		return r.GateRegistry.Resolve(tmpl.Plugin.Name)
	}
	return r.GateRegistry.Resolve(tmpl.Type)
}

func (r *PromotionTargetReconciler) nextAfterMetrics(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) (ctrl.Result, error) {
	if target.Gate != nil && target.Gate.Approval != nil && target.Gate.Approval.Required {
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseWaitingApproval)
		return ctrl.Result{Requeue: true}, nil
	}
	r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseApplying)
	return ctrl.Result{Requeue: true}, nil
}

func (r *PromotionTargetReconciler) handleWaitingApproval(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.PromotionTarget) (ctrl.Result, error) {
	if target.Rejected {
		rejectedBy := target.RejectedBy
		if rejectedBy == "" {
			rejectedBy = "unknown"
		}
		r.failTarget(ctx, promotionrun, target, fmt.Sprintf("rejected by %s", rejectedBy))
		return ctrl.Result{}, nil
	}

	if target.ApprovalSentAt == "" {
		r.sendApprovalNotification(ctx, promotionrun, target)
	}

	approvalGate, err := r.GateRegistry.Resolve("approval")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("approval gate: %w", err)
	}

	gateCtx := targetToGateContext(promotionrun, target, rt)
	result, err := approvalGate.Evaluate(ctx, gate.Request{Context: gateCtx, Policy: target.Gate})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("approval gate: %w", err)
	}

	log.FromContext(ctx).Info("approval gate", "phase", result.Phase, "target", target.Target)

	if result.IsPassed() {
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha1.TargetPhaseApplying)
		return ctrl.Result{Requeue: true}, nil
	}

	r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "WaitingApproval",
		"Waiting for Approval CR for %s", target.Target)
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func (r *PromotionTargetReconciler) sendApprovalNotification(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) {
	_ = ctx
	_ = promotionrun
	target.ApprovalSentAt = time.Now().UTC().Format(time.RFC3339)
}

func (r *PromotionTargetReconciler) handleApplying(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	desiredVersions := targetDesiredVersions(target)
	if len(desiredVersions) == 0 {
		r.failTarget(ctx, promotionrun, target, "target has no desired versions to apply")
		return ctrl.Result{}, nil
	}

	var mc kaprov1alpha1.FleetCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		target.MissingMCCount++
		if target.MissingMCCount >= missingMCFailThreshold {
			r.failTarget(ctx, promotionrun, target,
				fmt.Sprintf("FleetCluster %q not found after %d attempts", target.Target, target.MissingMCCount))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}
	target.MissingMCCount = 0
	if result, ok, err := r.requireFreshHeartbeat(ctx, promotionrun, target, &mc); err != nil || !ok {
		return result, err
	}
	if err := validateTargetTopology(&mc, desiredVersions); err != nil {
		r.failTarget(ctx, promotionrun, target, err.Error())
		return ctrl.Result{}, nil
	}

	// Claim active promotionrun on the FleetCluster.
	if mc.Status.ActivePromotionRun == "" || mc.Status.ActivePromotionRun == promotionrun.Name {
		if mc.Status.ActivePromotionRun == "" {
			patch := client.MergeFrom(mc.DeepCopy())
			mc.Status.ActivePromotionRun = promotionrun.Name
			if err := r.Status().Patch(ctx, &mc, patch); err != nil {
				return ctrl.Result{}, fmt.Errorf("claim activePromotionRun for %s: %w", mc.Name, err)
			}
		}
	} else {
		l.Info("cluster claimed by another promotionrun",
			"cluster", target.Target, "activePromotionRun", mc.Status.ActivePromotionRun)
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	capturePreviousVersions(target, &mc, desiredVersions)

	// Issue Apply exactly once per Applying entry.
	if r.ActuatorRegistry != nil && !target.ApplyIssued {
		act, err := r.ActuatorRegistry.Resolve(mc.Spec.Delivery.RegistryKey())
		if err != nil {
			l.Error(err, "failed to resolve actuator")
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		deltaCount, err := act.ApplyDelta(ctx, actuator.DeltaApplyRequest{
			Cluster:         &mc,
			DesiredVersions: desiredVersions,
		})
		if err != nil {
			l.Error(err, "Actuator.ApplyDelta failed, will retry")
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		target.ApplyIssued = true
		l.Info("Actuator.ApplyDelta issued", "cluster", target.Target, "deltaCount", deltaCount, "desiredVersions", desiredVersions)
	}

	// Poll for convergence.
	currentVersion := mc.Status.CurrentVersions[targetAppKey(target)] // nil map read returns "" safely
	if r.ActuatorRegistry != nil {
		act, err := r.ActuatorRegistry.Resolve(mc.Spec.Delivery.RegistryKey())
		if err != nil {
			l.Error(err, "failed to resolve actuator for convergence check")
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		if reporter, ok := act.(actuator.BackendObjectReporter); ok {
			objects, err := reporter.BackendObjects(ctx, &mc, desiredVersions)
			if err != nil {
				l.Error(err, "Actuator.BackendObjects failed, will retry")
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
			}
			target.BackendObjects = objects
		}
		converged, err := act.IsAllConverged(ctx, &mc, desiredVersions)
		if err != nil {
			l.Error(err, "Actuator.IsAllConverged failed, will retry")
			return ctrl.Result{RequeueAfter: requeueNormal}, nil
		}
		if converged {
			l.Info("cluster converged", "cluster", target.Target, "desiredVersions", desiredVersions)
			r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "Applied",
				"Desired versions applied to %s", target.Target)
			target.Phase = kaprov1alpha1.TargetPhaseConverged
			target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			return ctrl.Result{}, nil
		}
	}

	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		currentVersion == target.Version && len(desiredVersions) == 1 {
		l.Info("cluster converged", "cluster", target.Target, "version", target.Version)
		r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "Applied",
			"Version %s applied to %s", target.Version, target.Target)
		target.Phase = kaprov1alpha1.TargetPhaseConverged
		target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		return ctrl.Result{}, nil
	}

	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
		r.failTarget(ctx, promotionrun, target,
			fmt.Sprintf("cluster %s reported Failed phase", target.Target))
		return ctrl.Result{}, nil
	}

	// Warn when the cluster reports Converged but CurrentVersions is absent or stale —
	// this indicates the cluster-controller has not yet populated the version map.
	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseConverged && currentVersion != target.Version {
		l.Info("cluster Converged but CurrentVersions not yet updated — waiting for cluster-controller",
			"cluster", target.Target, "currentVersion", currentVersion, "wantVersion", target.Version)
	} else {
		l.Info("waiting for convergence",
			"cluster", target.Target, "clusterPhase", mc.Status.Phase,
			"currentVersion", currentVersion, "wantVersion", target.Version)
	}
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func capturePreviousVersions(target *kaprov1alpha1.TargetStatus, mc *kaprov1alpha1.FleetCluster, desiredVersions map[string]string) {
	if len(target.PreviousVersions) == 0 {
		target.PreviousVersions = make(map[string]string, len(desiredVersions))
		for appKey := range desiredVersions {
			if current := mc.Status.CurrentVersions[appKey]; current != "" {
				target.PreviousVersions[appKey] = current
			}
		}
	}
	if target.PreviousVersion == "" {
		target.PreviousVersion = target.PreviousVersions[targetAppKey(target)]
	}
}

func validateTargetTopology(mc *kaprov1alpha1.FleetCluster, desiredVersions map[string]string) error {
	if len(desiredVersions) <= 1 || mc.Spec.Delivery.Mode != kaprov1alpha1.DeliveryModePull || mc.Spec.Delivery.BackendRef != "flux" {
		return nil
	}
	for appKey := range desiredVersions {
		if mc.Spec.Delivery.Parameters["ociRepository."+appKey] == "" {
			return fmt.Errorf("cluster %s is missing delivery.parameters[%q] required for multi-artifact flux delivery", mc.Name, "ociRepository."+appKey)
		}
	}
	return nil
}

// transitionTo mutates target.Phase and related timestamps in-place.
// Events are emitted on BOTH the parent PromotionRun (fleet-level view in k9s :promotionrun describe)
// and the PromotionTarget itself (per-target CI-log view in k9s :promotiontarget describe).
func (r *PromotionTargetReconciler) transitionTo(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, phase kaprov1alpha1.TargetPhase) {
	prevPhase := target.Phase
	target.Phase = phase
	if phase == kaprov1alpha1.TargetPhaseSoaking && target.StartedAt == "" {
		target.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if phase == kaprov1alpha1.TargetPhaseApplying {
		target.ApplyIssued = false
	}
	target.PhaseEnteredAt = time.Now().UTC().Format(time.RFC3339)

	msg := phaseTransitionMessage(prevPhase, phase, target)

	// Event on PromotionRun — fleet-level log (visible in :promotionrun describe)
	r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "PhaseTransition",
		"[%s/%s] %s → %s: %s", target.Stage, target.Target, prevPhase, phase, msg)

	// Event on PromotionTarget — per-target CI log (visible in :promotiontarget describe)
	// The PromotionTarget object is retrieved from context when available.
	if rt := promotionTargetFromContext(ctx); rt != nil {
		r.Recorder.Eventf(rt, corev1.EventTypeNormal, string(phase), msg)
	}
}

// phaseTransitionMessage returns a human-readable message for the FSM transition,
// giving operators CI-promotionplan-like context when reading k9s events.
func phaseTransitionMessage(from kaprov1alpha1.TargetPhase, to kaprov1alpha1.TargetPhase, target *kaprov1alpha1.TargetStatus) string {
	switch to {
	case kaprov1alpha1.TargetPhasePending:
		return fmt.Sprintf("queued for delivery of %s", target.Version)
	case kaprov1alpha1.TargetPhaseVerification:
		return fmt.Sprintf("verifying artifact signature for %s", target.Version)
	case kaprov1alpha1.TargetPhaseHealthCheck:
		return "checking pre-deployment cluster health"
	case kaprov1alpha1.TargetPhaseSoaking:
		if target.Gate != nil && target.Gate.Gate.SoakTime != "" {
			return fmt.Sprintf("soak timer started (%s)", target.Gate.Gate.SoakTime)
		}
		return "soak timer started"
	case kaprov1alpha1.TargetPhaseMetricsCheck:
		return "evaluating metrics gates"
	case kaprov1alpha1.TargetPhaseWaitingApproval:
		return "waiting for human approval"
	case kaprov1alpha1.TargetPhaseApplying:
		return fmt.Sprintf("applying version %s to cluster", target.Version)
	case kaprov1alpha1.TargetPhaseConverged:
		return fmt.Sprintf("cluster converged on %s", target.Version)
	case kaprov1alpha1.TargetPhaseFailed:
		if target.Message != "" {
			return target.Message
		}
		return "target failed"
	case kaprov1alpha1.TargetPhaseSkipped:
		return "target skipped (onFailure=continue)"
	default:
		return string(to)
	}
}

// failTarget records a failure and applies the onFailure policy.
func (r *PromotionTargetReconciler) failTarget(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, msg string) {
	target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	target.Message = msg

	onFailure := "halt"
	if target.Gate != nil && target.Gate.OnFailure != "" {
		onFailure = target.Gate.OnFailure
	}

	if onFailure == "continue" {
		target.Phase = kaprov1alpha1.TargetPhaseSkipped
		r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "TargetSkipped",
			"[%s/%s] skipped (onFailure=continue): %s", target.Stage, target.Target, msg)
		if rt := promotionTargetFromContext(ctx); rt != nil {
			r.Recorder.Eventf(rt, corev1.EventTypeWarning, "Skipped", "skipped: %s", msg)
		}
		return
	}

	target.Phase = kaprov1alpha1.TargetPhaseFailed
	r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "TargetFailed",
		"[%s/%s] failed: %s", target.Stage, target.Target, msg)
	if rt := promotionTargetFromContext(ctx); rt != nil {
		r.Recorder.Eventf(rt, corev1.EventTypeWarning, "Failed", "failed: %s", msg)
	}

	// Rollback is triggered by the parent PromotionRunReconciler when it aggregates
	// child statuses and detects a Failed target with onFailure=rollback.
	// The child never creates sibling PromotionTarget objects.
}

func (r *PromotionTargetReconciler) notifyPersistedTransitions(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, previous, current *kaprov1alpha1.TargetStatus) {
	if r.Notifier == nil {
		return
	}

	prevPhase := previous.Phase
	currPhase := current.Phase
	if prevPhase != currPhase && currPhase != kaprov1alpha1.TargetPhaseWaitingApproval {
		r.Notifier.Notify(ctx, notification.Event{
			Type:          eventTypeForPhase(currPhase),
			Phase:         string(currPhase),
			Version:       current.Version,
			Target:        current.Target,
			PromotionRun:  promotionrun.Name,
			PromotionPlan: current.PromotionPlanRef,
			Stage:         current.Stage,
			Message:       current.Message,
			IsFailure:     currPhase == kaprov1alpha1.TargetPhaseFailed,
		}, notificationPolicyFrom(current.Gate))
	}

	if previous.ApprovalSentAt == "" && current.ApprovalSentAt != "" {
		r.notifyApprovalRequest(ctx, promotionrun, current)
	}
}

func (r *PromotionTargetReconciler) notifyApprovalRequest(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) {
	var approveURL, rejectURL string
	if len(r.ApprovalSecret) > 0 && r.ExternalURL != "" {
		var err error
		approveURL, rejectURL, err = buildApprovalURLs(r.ExternalURL, r.ApprovalSecret, promotionrun, target)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to build approval URLs")
		}
	}

	r.Notifier.Notify(ctx, notification.Event{
		Type:          notification.EventApprovalRequired,
		Phase:         string(kaprov1alpha1.TargetPhaseWaitingApproval),
		Version:       target.Version,
		Target:        target.Target,
		PromotionRun:  promotionrun.Name,
		PromotionPlan: target.PromotionPlanRef,
		Stage:         target.Stage,
		Message:       "Approval required to proceed",
		ApproveURL:    approveURL,
		RejectURL:     rejectURL,
	}, notificationPolicyFrom(target.Gate))
}

// --- SetupWithManager & watch mappers ---

func (r *PromotionTargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	forPredicates := []predicate.Predicate{promotionTargetPredicates()}
	if r.ShardPredicate != nil {
		forPredicates = append(forPredicates, r.ShardPredicate)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 5*time.Minute),
		}).
		For(&kaprov1alpha1.PromotionTarget{}, builder.WithPredicates(forPredicates...)).
		Watches(
			&kaprov1alpha1.Approval{},
			handler.EnqueueRequestsFromMapFunc(promotionTargetsForApproval),
		).
		Watches(
			&kaprov1alpha1.FleetCluster{},
			handler.EnqueueRequestsFromMapFunc(r.promotionTargetsForFleetCluster),
			builder.WithPredicates(promotionTargetFleetClusterPredicates()),
		).
		Watches(
			&coordinationv1.Lease{},
			handler.EnqueueRequestsFromMapFunc(r.promotionTargetsForHeartbeatLease),
			builder.WithPredicates(heartbeatLeasePredicates()),
		).
		Complete(r)
}

func promotionTargetPredicates() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRT, okOld := e.ObjectOld.(*kaprov1alpha1.PromotionTarget)
			newRT, okNew := e.ObjectNew.(*kaprov1alpha1.PromotionTarget)
			if !okOld || !okNew {
				return true
			}
			if oldRT.GetGeneration() != newRT.GetGeneration() {
				return true
			}
			if !maps.Equal(oldRT.GetAnnotations(), newRT.GetAnnotations()) {
				return true
			}
			return oldRT.Status.Rejected != newRT.Status.Rejected ||
				oldRT.Status.RejectedBy != newRT.Status.RejectedBy
		},
	}
}

func promotionTargetsForApproval(_ context.Context, obj client.Object) []ctrl.Request {
	approval, ok := obj.(*kaprov1alpha1.Approval)
	if !ok || approval.Spec.Ref == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: approval.Spec.Ref}}}
}

func promotionTargetFleetClusterPredicates() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldMC, okOld := e.ObjectOld.(*kaprov1alpha1.FleetCluster)
			newMC, okNew := e.ObjectNew.(*kaprov1alpha1.FleetCluster)
			if !okOld || !okNew {
				return true
			}
			if oldMC.GetGeneration() != newMC.GetGeneration() {
				return true
			}
			return !fleetClusterStatusEqualForRollouts(oldMC.Status, newMC.Status)
		},
	}
}

func (r *PromotionTargetReconciler) promotionTargetsForFleetCluster(ctx context.Context, obj client.Object) []ctrl.Request {
	mc, ok := obj.(*kaprov1alpha1.FleetCluster)
	if !ok {
		return nil
	}
	var targetList kaprov1alpha1.PromotionTargetList
	if err := r.List(ctx, &targetList,
		client.MatchingFields{IndexKeyActiveCluster: mc.Name},
	); err != nil {
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(targetList.Items))
	for i := range targetList.Items {
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&targetList.Items[i])})
	}
	return reqs
}

func heartbeatLeasePredicates() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isHeartbeatLeaseObject(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return isHeartbeatLeaseObject(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isHeartbeatLeaseObject(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldLease, okOld := e.ObjectOld.(*coordinationv1.Lease)
			newLease, okNew := e.ObjectNew.(*coordinationv1.Lease)
			if !okOld || !okNew || !isHeartbeatLeaseObject(newLease) {
				return false
			}
			oldFresh := leaseIsFresh(oldLease, heartbeatFreshTimeout)
			newFresh := leaseIsFresh(newLease, heartbeatFreshTimeout)
			return oldFresh != newFresh
		},
	}
}

func isHeartbeatLeaseObject(obj client.Object) bool {
	return obj != nil && strings.HasPrefix(obj.GetName(), heartbeatLeasePrefix)
}

func leaseIsFresh(lease *coordinationv1.Lease, timeout time.Duration) bool {
	observed, ok := leaseHeartbeatTime(lease)
	return ok && time.Since(observed) < timeout
}

func (r *PromotionTargetReconciler) promotionTargetsForHeartbeatLease(ctx context.Context, obj client.Object) []ctrl.Request {
	if !isHeartbeatLeaseObject(obj) {
		return nil
	}
	clusterName := strings.TrimPrefix(obj.GetName(), heartbeatLeasePrefix)
	var targetList kaprov1alpha1.PromotionTargetList
	if err := r.List(ctx, &targetList,
		client.MatchingFields{IndexKeyActiveCluster: clusterName},
	); err != nil {
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(targetList.Items))
	for i := range targetList.Items {
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&targetList.Items[i])})
	}
	return reqs
}

func (r *PromotionTargetReconciler) updatePromotionTargetStatusContract(rt *kaprov1alpha1.PromotionTarget) {
	rt.Status.ObservedGeneration = rt.Generation

	phase := rt.Status.Phase
	switch phase {
	case kaprov1alpha1.TargetPhaseConverged:
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionTrue, "Converged", "target converged")
		r.setPromotionTargetCondition(rt, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Converged", "target converged")
		apimeta.RemoveStatusCondition(&rt.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	case kaprov1alpha1.TargetPhaseFailed:
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionFalse, "Failed", rt.Status.Message)
		r.setPromotionTargetCondition(rt, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Failed", "target failed")
		r.setPromotionTargetCondition(rt, kaprov1alpha1.ConditionTypeStalled, metav1.ConditionTrue, "Failed", rt.Status.Message)
	case kaprov1alpha1.TargetPhaseSkipped:
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionFalse, "Skipped", rt.Status.Message)
		r.setPromotionTargetCondition(rt, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Skipped", "target skipped")
		apimeta.RemoveStatusCondition(&rt.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	default:
		message := "target is progressing"
		if rt.Status.Message != "" {
			message = rt.Status.Message
		}
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionFalse, string(phase), message)
		r.setPromotionTargetCondition(rt, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionTrue, string(phase), message)
		apimeta.RemoveStatusCondition(&rt.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	}
}

func (r *PromotionTargetReconciler) syncPromotionTargetPhaseLabel(ctx context.Context, rt *kaprov1alpha1.PromotionTarget) error {
	phase := rt.Status.Phase
	if phase == "" {
		phase = kaprov1alpha1.TargetPhasePending
	}
	if rt.Labels != nil && rt.Labels["kapro.io/phase"] == string(phase) {
		return nil
	}
	patch := client.MergeFrom(rt.DeepCopy())
	if rt.Labels == nil {
		rt.Labels = make(map[string]string)
	}
	// Keep phase in metadata for k9s label filtering:
	//   :promotiontarget /phase=WaitingApproval  or  /stage=canary
	rt.Labels["kapro.io/phase"] = string(phase)
	return r.Patch(ctx, rt, patch)
}

func (r *PromotionTargetReconciler) setPromotionTargetCondition(rt *kaprov1alpha1.PromotionTarget, conditionType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: rt.Generation,
		LastTransitionTime: metav1.Now(),
	})
}
