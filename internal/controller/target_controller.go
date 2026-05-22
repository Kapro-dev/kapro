package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/promotion/fsm"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
)

const maxImmediatePhaseAdvances = 8

// contextKeyPromotionTarget is used to pass the PromotionTarget object through
// context so FSM transition helpers can emit events on the target itself.
type contextKeyPromotionTargetType struct{}

var contextKeyPromotionTarget = contextKeyPromotionTargetType{}

func contextWithPromotionTarget(ctx context.Context, rt *kaprov1alpha2.Target) context.Context {
	return context.WithValue(ctx, contextKeyPromotionTarget, rt)
}

func promotionTargetFromContext(ctx context.Context) *kaprov1alpha2.Target {
	rt, _ := ctx.Value(contextKeyPromotionTarget).(*kaprov1alpha2.Target)
	return rt
}

// TargetReconciler independently advances one PromotionTarget through the
// per-target rollout FSM. It reads the parent PromotionRun (read-only, for suspend
// and version info) and the referenced FleetCluster, but NEVER loads sibling
// PromotionTargets or writes outside its own PromotionTarget.status and
// metadata.labels["kapro.io/phase"].
//
// The parent PromotionRunReconciler aggregates child statuses via indexed queries —
// it never runs the FSM itself.
type TargetReconciler struct {
	client.Client
	Recorder         record.EventRecorder
	Scheme           *runtime.Scheme
	ActuatorRegistry *actuator.Registry
	Notifier         notification.Notifier
	ApprovalSecret   []byte
	ExternalURL      string
	GateRegistry     *gate.Registry

	// StagePublisher emits kapro.io/promotion.stage.gate.* events to the
	// operator-level CloudEvents sink on gate evaluation transitions.
	// Nil-safe — when unset, no gate-narrative events fire.
	StagePublisher StageEventPublisher

	// ShardPredicate optionally filters objects by shard label for horizontal scaling.
	// When nil, all objects are processed.
	ShardPredicate predicate.Predicate

	// fsmMachine is the per-phase dispatch table for target rollout. Built
	// lazily on first Reconcile via fsmOnce so that unit tests which
	// construct TargetReconciler directly (without calling
	// SetupWithManager) still get the FSM wired.
	fsmOnce    sync.Once
	fsmMachine *fsm.Machine[kaprov1alpha2.TargetPhase, *fsmEnv]
}

// +kubebuilder:rbac:groups=kapro.io,resources=targets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=targets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get
// +kubebuilder:rbac:groups=kapro.io,resources=clusters,verbs=get
// +kubebuilder:rbac:groups=kapro.io,resources=clusters/status,verbs=get;patch

func (r *TargetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	resultLabel := "success"
	defer func() {
		kaprometrics.ControllerReconciles.WithLabelValues("promotion_target", resultLabel).Inc()
		kaprometrics.ControllerReconcileDuration.WithLabelValues("promotion_target").Observe(time.Since(start).Seconds())
	}()

	var rt kaprov1alpha2.Target
	if err := r.Get(ctx, req.NamespacedName, &rt); err != nil {
		resultLabel = "error"
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	prevTarget := rt.Status.TargetExecutionState

	if !rt.Spec.Cancelled {
		phase := rt.Status.Phase
		switch phase {
		case kaprov1alpha2.TargetPhaseConverged, kaprov1alpha2.TargetPhaseFailed, kaprov1alpha2.TargetPhaseSkipped:
			if updateErr := r.syncPromotionTargetPhaseLabel(ctx, &rt); updateErr != nil {
				kaprometrics.StatusWrites.WithLabelValues("promotiontarget", "error").Inc()
				resultLabel = "error"
				return ctrl.Result{}, fmt.Errorf("patch terminal PromotionTarget phase label %s: %w", rt.Name, updateErr)
			}
			return ctrl.Result{}, nil
		}
	}

	// Read parent PromotionRun — read-only, never mutated here.
	var promotionrun kaprov1alpha2.PromotionRun
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
		// gate-IsConflict: cancellation is a deterministic, side-effect-free
		// status flip — perfect candidate for the retry helper. The mutate
		// function is idempotent so a refetch + re-apply on conflict is safe.
		cancelReason := rt.Spec.CancelledReason
		cancelPhase := rt.Spec.CancelledPhase
		if cancelPhase == "" {
			cancelPhase = kaprov1alpha2.TargetPhaseFailed
		}
		nowStr := time.Now().UTC().Format(time.RFC3339)
		if updateErr := StatusUpdateWithRetry(ctx, r.Client, &rt, func(fresh *kaprov1alpha2.Target) error {
			fresh.Status.Phase = cancelPhase
			fresh.Status.Message = "cancelled: " + cancelReason
			fresh.Status.FinishedAt = nowStr
			r.updatePromotionTargetStatusContract(fresh)
			return nil
		}); updateErr != nil {
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
	rt.Status.TargetExecutionState = target
	r.updatePromotionTargetStatusContract(&rt)
	if updateErr := r.Status().Update(ctx, &rt); updateErr != nil {
		// gate-IsConflict: don't bury the FSM in a refetch-and-retry loop —
		// the FSM mutation cost is too high to repeat per conflict. Instead,
		// detect Conflict explicitly and return a fast requeue (1s) so the
		// workqueue picks the request back up with a fresh read; non-conflict
		// errors bubble up as before. At 500-cluster scale this turns a
		// "flapping condition for ~30s of exponential backoff" into a
		// predictable 1s retry that keeps p99 latency bounded.
		if apierrors.IsConflict(updateErr) {
			kaprometrics.StatusWrites.WithLabelValues("promotiontarget", "conflict").Inc()
			resultLabel = "conflict"
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
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

func (r *TargetReconciler) advanceTargetUntilStable(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, rt *kaprov1alpha2.Target) (ctrl.Result, error) {
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
		if target.Phase == kaprov1alpha2.TargetPhaseApplying {
			return result, nil
		}
	}
	return ctrl.Result{Requeue: true}, nil
}

func isImmediateRequeue(result ctrl.Result) bool {
	return result.Requeue && result.RequeueAfter == 0 //nolint:staticcheck // SA1019: result.Requeue deprecated but replacement needs larger refactor
}

// fsmEnv is the per-tick state bag the FSM hands to each phase handler.
// Only the values that vary between phase ticks live here; the reconciler
// itself is captured once by the closures registered in buildFSM (it is
// stable for the lifetime of the controller).
type fsmEnv struct {
	promotionrun *kaprov1alpha2.PromotionRun
	target       *kaprov1alpha2.TargetExecutionState
	rt           *kaprov1alpha2.Target
}

// buildFSM constructs the phase dispatch table. The closures capture r,
// which is stable for the reconciler's lifetime; per-tick values
// (promotionrun / target / rt) are passed through fsmEnv at Step time.
// Called exactly once per reconciler via ensureFSM.
func (r *TargetReconciler) buildFSM() *fsm.Machine[kaprov1alpha2.TargetPhase, *fsmEnv] {
	m := fsm.New[kaprov1alpha2.TargetPhase, *fsmEnv]()

	// Declared phase graph (D3-PR2). The AllowedNext metadata on every
	// Register is what transitionTo consults via ValidateTransition to
	// flag undeclared transitions. The graph below IS the documentation —
	// promotiontarget_fsm_graph_test.go asserts the metadata matches
	// this comment, so they cannot drift:
	//
	//   ""              → Pending
	//   Pending         → Verification, Failed, Skipped
	//   Verification    → HealthCheck,  Failed, Skipped
	//   HealthCheck     → Soaking, MetricsCheck, Failed, Skipped
	//   Soaking         → MetricsCheck, Failed, Skipped
	//   MetricsCheck    → WaitingApproval, Applying, Failed, Skipped
	//   WaitingApproval → Applying, Failed, Skipped
	//   Applying        → Converged, Failed, Skipped
	//   Converged | Failed | Skipped — terminal (filtered before reaching FSM)
	//
	// Failed/Skipped appear in AllowedNext for every non-terminal phase
	// because failTarget can fire from inside any handler; ValidateTransition
	// treats transitions TO terminal phases as always-allowed anyway, but
	// listing them keeps the table self-documenting.
	terminal := []kaprov1alpha2.TargetPhase{
		kaprov1alpha2.TargetPhaseFailed,
		kaprov1alpha2.TargetPhaseSkipped,
	}
	must(m.RegisterInitial(func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
		r.transitionTo(ctx, e.promotionrun, e.target, kaprov1alpha2.TargetPhasePending)
		return ctrl.Result{Requeue: true}, nil //nolint:staticcheck // SA1019: result.Requeue deprecated, see existing notes
	}, kaprov1alpha2.TargetPhasePending))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.TargetPhase, *fsmEnv]{
		Phase: kaprov1alpha2.TargetPhasePending,
		AllowedNext: append([]kaprov1alpha2.TargetPhase{
			kaprov1alpha2.TargetPhaseVerification,
		}, terminal...),
		Handler: func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
			return r.handlePending(ctx, e.promotionrun, e.target)
		},
	}))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.TargetPhase, *fsmEnv]{
		Phase: kaprov1alpha2.TargetPhaseVerification,
		AllowedNext: append([]kaprov1alpha2.TargetPhase{
			kaprov1alpha2.TargetPhaseHealthCheck,
		}, terminal...),
		Handler: func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
			return r.handleVerification(ctx, e.promotionrun, e.target, e.rt)
		},
	}))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.TargetPhase, *fsmEnv]{
		Phase: kaprov1alpha2.TargetPhaseHealthCheck,
		AllowedNext: append([]kaprov1alpha2.TargetPhase{
			kaprov1alpha2.TargetPhaseSoaking,
			kaprov1alpha2.TargetPhaseMetricsCheck,
		}, terminal...),
		Handler: func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
			return r.handleHealthCheck(ctx, e.promotionrun, e.target)
		},
	}))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.TargetPhase, *fsmEnv]{
		Phase: kaprov1alpha2.TargetPhaseSoaking,
		AllowedNext: append([]kaprov1alpha2.TargetPhase{
			kaprov1alpha2.TargetPhaseMetricsCheck,
		}, terminal...),
		Handler: func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
			return r.handleSoaking(ctx, e.promotionrun, e.target, e.rt)
		},
	}))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.TargetPhase, *fsmEnv]{
		Phase: kaprov1alpha2.TargetPhaseMetricsCheck,
		AllowedNext: append([]kaprov1alpha2.TargetPhase{
			kaprov1alpha2.TargetPhaseWaitingApproval,
			kaprov1alpha2.TargetPhaseApplying,
		}, terminal...),
		Handler: func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
			return r.handleMetricsCheck(ctx, e.promotionrun, e.target, e.rt)
		},
	}))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.TargetPhase, *fsmEnv]{
		Phase: kaprov1alpha2.TargetPhaseWaitingApproval,
		AllowedNext: append([]kaprov1alpha2.TargetPhase{
			kaprov1alpha2.TargetPhaseApplying,
		}, terminal...),
		Handler: func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
			return r.handleWaitingApproval(ctx, e.promotionrun, e.target, e.rt)
		},
	}))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.TargetPhase, *fsmEnv]{
		Phase: kaprov1alpha2.TargetPhaseApplying,
		AllowedNext: append([]kaprov1alpha2.TargetPhase{
			kaprov1alpha2.TargetPhaseConverged,
		}, terminal...),
		Handler: func(ctx context.Context, e *fsmEnv) (ctrl.Result, error) {
			return r.handleApplying(ctx, e.promotionrun, e.target)
		},
	}))
	must(m.RegisterTerminal(
		kaprov1alpha2.TargetPhaseConverged,
		kaprov1alpha2.TargetPhaseFailed,
		kaprov1alpha2.TargetPhaseSkipped,
	))
	return m
}

// must panics when the FSM rejects a registration. Wrapping every Register
// call in if err != nil { return ctrl.Result{}, err } would clutter the
// table; registration failure is a programmer bug (duplicate phase, nil
// handler) caught at init, not at runtime, so a panic is correct.
func must(err error) {
	if err != nil {
		panic(err)
	}
}

// ensureFSM builds the phase dispatch table the first time it is called
// on this reconciler. Lazy init keeps unit tests that construct
// TargetReconciler directly (without SetupWithManager) working.
func (r *TargetReconciler) ensureFSM() {
	r.fsmOnce.Do(func() {
		r.fsmMachine = r.buildFSM()
	})
}

// advanceTarget dispatches one FSM step for the given target. The legacy
// implementation was a switch statement; this delegates to the cached
// Machine built by ensureFSM so the dispatch is declarative (Phases()
// lists everything supported) and no table is rebuilt per phase tick.
func (r *TargetReconciler) advanceTarget(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, rt *kaprov1alpha2.Target) (ctrl.Result, error) {
	r.ensureFSM()
	return r.fsmMachine.Step(ctx, target.Phase, &fsmEnv{
		promotionrun: promotionrun,
		target:       target,
		rt:           rt,
	})
}

func (r *TargetReconciler) handlePending(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState) (ctrl.Result, error) {
	var mc kaprov1alpha2.Cluster
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
	r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseVerification)
	return ctrl.Result{Requeue: true}, nil
}

func (r *TargetReconciler) handleVerification(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, rt *kaprov1alpha2.Target) (ctrl.Result, error) {
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
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseHealthCheck)
		return ctrl.Result{Requeue: true}, nil
	}
	if result.Phase == kaprov1alpha2.GatePhaseFailed {
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

func (r *TargetReconciler) handleHealthCheck(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var mc kaprov1alpha2.Cluster
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

func (r *TargetReconciler) transitionToSoakOrMetrics(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState) (ctrl.Result, error) {
	if target.Gate == nil || target.Gate.Gate.SoakTime == "" {
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}
	r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseSoaking)
	return ctrl.Result{Requeue: true}, nil
}

func (r *TargetReconciler) handleSoaking(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, rt *kaprov1alpha2.Target) (ctrl.Result, error) {
	if target.Gate == nil {
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseMetricsCheck)
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
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseMetricsCheck)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
}

func (r *TargetReconciler) handleMetricsCheck(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, rt *kaprov1alpha2.Target) (ctrl.Result, error) {
	policy := target.Gate

	if policy == nil || (len(policy.Gate.Metrics) == 0 && len(policy.Gate.Templates) == 0) {
		return r.nextAfterMetrics(ctx, promotionrun, target)
	}

	gateCtx := targetToGateContext(promotionrun, target, rt)
	now := time.Now().UTC().Format(time.RFC3339)
	gates := target.Gates
	if gates == nil {
		gates = make([]kaprov1alpha2.GateRunStatus, 0, len(policy.Gate.Metrics)+len(policy.Gate.Templates))
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
				phase = kaprov1alpha2.GatePhaseInconclusive
			}
			gateStatus.Phase = phase
			gateStatus.Message = result.Message
			gateStatus.Attempts++
			gateStatus.VendorRef = result.VendorRef
			gateStatus.Evidence = toAPIGateEvidence(result.Evidence)
			if len(result.Results) > 0 {
				gateStatus.Results = toAPIConditionResults(result.Results)
			}
			if phase != kaprov1alpha2.GatePhaseRunning && phase != kaprov1alpha2.GatePhasePending && phase != kaprov1alpha2.GatePhaseInconclusive {
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
func metricsGateTimedOut(target *kaprov1alpha2.TargetExecutionState, policy *kaprov1alpha2.GatePolicySpec) (bool, string) {
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

func (r *TargetReconciler) evaluateGateTemplates(
	ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun,
	target *kaprov1alpha2.TargetExecutionState,
	gateCtx *gate.Context,
	policy *kaprov1alpha2.GatePolicySpec,
) (bool, time.Duration, error) {
	l := log.FromContext(ctx)
	now := time.Now().UTC().Format(time.RFC3339)
	gates := target.Gates
	if gates == nil {
		gates = make([]kaprov1alpha2.GateRunStatus, 0, len(policy.Gate.Templates))
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
		// kapro.io/promotion.stage.gate.waiting fires exactly once when
		// this gate first enters evaluation for this (target, gate)
		// tuple. Attempts==0 just before Evaluate is the canonical
		// "first time we see this gate" signal.
		firstEvaluation := gateStatus.Attempts == 0 &&
			gateStatus.Phase != kaprov1alpha2.GatePhasePassed &&
			gateStatus.Phase != kaprov1alpha2.GatePhaseFailed
		if firstEvaluation {
			r.publishGateEvent(ctx, promotionrun, target, gateName, "waiting",
				string(kaprov1alpha2.GatePhaseRunning), "gate evaluation started", "")
		}
		if gateStatus.Phase == kaprov1alpha2.GatePhasePassed {
			continue
		}
		if gateStatus.Phase == kaprov1alpha2.GatePhaseFailed {
			allPassed = false
			continue
		}

		args := resolveSyncArgs(tmpl, gateCtx)
		g, err := r.gateForTemplate(tmpl)
		if err != nil {
			return false, 0, fmt.Errorf("gate for template %q: %w", gateName, err)
		}

		result, err := g.Evaluate(ctx, gate.Request{
			Context:      gateCtx,
			Template:     tmpl,
			Args:         args,
			Fleet:        promotionrun.Labels["kapro.io/fleet"],
			Promotion:    promotionrun.Labels["kapro.io/promotion"],
			PromotionRun: promotionrun.Name,
			Plan:         target.PlanRef,
			Stage:        target.Stage,
			Target:       target.Target,
			Version:      target.Version,
			Parameters:   args,
			Logger: log.FromContext(ctx).WithValues(
				"fleet", promotionrun.Labels["kapro.io/fleet"],
				"promotion", promotionrun.Labels["kapro.io/promotion"],
				"promotionrun", promotionrun.Name,
				"plan", target.PlanRef,
				"stage", target.Stage,
				"target", target.Target,
			),
		})
		maxAttempts := tmpl.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		if err != nil {
			l.Error(err, "gate template evaluation error, will retry", "gate", gateName)
			kaprometrics.GateEvaluations.WithLabelValues(gateMetricName(tmpl), "error").Inc()
			gateStatus.Phase = kaprov1alpha2.GatePhaseRunning
			gateStatus.Message = err.Error()
			gateStatus.Attempts++
			if gateStatus.Attempts >= maxAttempts {
				gateStatus.Phase = kaprov1alpha2.GatePhaseFailed
				gateStatus.Message = fmt.Sprintf("gate %s exceeded maxAttempts=%d after evaluation errors: %s", gateName, maxAttempts, err)
				gateStatus.FinishedAt = now
			}
			setGateStatus(&gates, gateStatus)
			allPassed = false
			if gateStatus.Phase == kaprov1alpha2.GatePhaseFailed {
				continue
			}
			continue
		}

		phase := result.Phase
		if phase == "" {
			phase = kaprov1alpha2.GatePhaseInconclusive
		}
		kaprometrics.GateEvaluations.WithLabelValues(gateMetricName(tmpl), gateMetricResult(phase)).Inc()

		gateStatus.Phase = phase
		gateStatus.Message = result.Message
		gateStatus.Attempts++
		gateStatus.VendorRef = result.VendorRef
		gateStatus.Evidence = toAPIGateEvidence(result.Evidence)
		if len(result.Results) > 0 {
			gateStatus.Results = toAPIConditionResults(result.Results)
		}
		if phase != "" && phase != kaprov1alpha2.GatePhaseRunning && phase != kaprov1alpha2.GatePhasePending {
			gateStatus.FinishedAt = now
		}
		attemptsExhausted := gateStatus.Attempts >= maxAttempts && phase != kaprov1alpha2.GatePhasePassed
		if attemptsExhausted {
			phase = kaprov1alpha2.GatePhaseFailed
			gateStatus.Phase = kaprov1alpha2.GatePhaseFailed
			gateStatus.Message = fmt.Sprintf("gate %s exceeded maxAttempts=%d", gateName, maxAttempts)
			gateStatus.FinishedAt = now
		}
		setGateStatus(&gates, gateStatus)

		l.Info("gate template evaluated", "gate", gateName, "phase", phase, "target", target.Target)
		r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "GateEvaluated",
			"gate %s for %s: %s — %s", gateName, target.Target, phase, result.Message)

		switch phase {
		case kaprov1alpha2.GatePhaseFailed:
			switch tmpl.FailurePolicy {
			case "skip":
				gateStatus.Phase = kaprov1alpha2.GatePhasePassed
				gateStatus.Message = "skipped (failurePolicy=skip)"
				gateStatus.FinishedAt = now
				setGateStatus(&gates, gateStatus)
				continue
			case "retry":
				if !attemptsExhausted {
					gateStatus.Phase = kaprov1alpha2.GatePhaseRunning
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
			r.publishGateEvent(ctx, promotionrun, target, gateName, "failed",
				string(kaprov1alpha2.GatePhaseFailed), "gate evaluation failed", gateStatus.Message)
			allPassed = false
		case kaprov1alpha2.GatePhaseInconclusive:
			switch tmpl.InconclusivePolicy {
			case "skip":
				gateStatus.Phase = kaprov1alpha2.GatePhasePassed
				gateStatus.Message = "skipped (inconclusivePolicy=skip)"
				gateStatus.FinishedAt = now
				setGateStatus(&gates, gateStatus)
				continue
			case "halt":
				gateStatus.Phase = kaprov1alpha2.GatePhaseFailed
				gateStatus.FinishedAt = now
				setGateStatus(&gates, gateStatus)
				allPassed = false
				continue
			}
			allPassed = false
			if d := parseDurationOrDefault(result.RetryAfter); d < requeueAfter || requeueAfter == requeueNormal {
				requeueAfter = d
			}
		case kaprov1alpha2.GatePhaseRunning, kaprov1alpha2.GatePhasePending:
			allPassed = false
			if d := parseDurationOrDefault(result.RetryAfter); d < requeueAfter || requeueAfter == requeueNormal {
				requeueAfter = d
			}
		case kaprov1alpha2.GatePhasePassed:
			r.notifyGateEvent(ctx, promotionrun, target, notification.EventGatePassed, gateName, gateStatus.Message, false)
			r.publishGateEvent(ctx, promotionrun, target, gateName, "passed",
				string(kaprov1alpha2.GatePhasePassed), "gate passed", gateStatus.Message)
		}
	}

	target.Gates = gates
	return allPassed, requeueAfter, nil
}

// publishGateEvent forwards a kapro.io/promotion.stage.gate.* emission
// to the operator-level CloudEvents sink. Nil-safe.
func (r *TargetReconciler) publishGateEvent(ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState,
	gateName, kind, phase, reason, message string) {
	if r.StagePublisher == nil || promotionrun == nil || target == nil {
		return
	}
	r.StagePublisher.PublishGateEvent(ctx, promotionrun,
		target.PlanRef, target.Stage, gateName, target.Target,
		kind, phase, reason, message)
}

func (r *TargetReconciler) notifyGateEvent(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, eventType, gateName, message string, isFailure bool) {
	if r.Notifier == nil {
		return
	}
	if message == "" {
		message = gateName
	}
	r.Notifier.Notify(ctx, notification.Event{
		Type:         eventType,
		Phase:        string(target.Phase),
		Version:      target.Version,
		Target:       target.Target,
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Message:      fmt.Sprintf("%s: %s", gateName, message),
		IsFailure:    isFailure,
	}, notificationPolicyFrom(target.Gate))
}

func (r *TargetReconciler) gateForTemplate(tmpl *kaprov1alpha2.GateTemplateSpec) (gate.Gate, error) {
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

func gateMetricName(tmpl *kaprov1alpha2.GateTemplateSpec) string {
	if tmpl == nil {
		return "unknown"
	}
	if tmpl.Type == "plugin" && tmpl.Plugin != nil && tmpl.Plugin.Name != "" {
		return tmpl.Plugin.Name
	}
	return tmpl.Type
}

func gateMetricResult(phase kaprov1alpha2.GatePhase) string {
	switch phase {
	case kaprov1alpha2.GatePhasePassed:
		return "passed"
	case kaprov1alpha2.GatePhaseFailed:
		return "failed"
	case kaprov1alpha2.GatePhasePending:
		return "pending"
	case kaprov1alpha2.GatePhaseRunning:
		return "running"
	case kaprov1alpha2.GatePhaseInconclusive:
		return "inconclusive"
	default:
		return "unknown"
	}
}

func (r *TargetReconciler) nextAfterMetrics(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState) (ctrl.Result, error) {
	if target.Gate != nil && target.Gate.Approval != nil && target.Gate.Approval.Required {
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseWaitingApproval)
		return ctrl.Result{Requeue: true}, nil
	}
	r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseApplying)
	return ctrl.Result{Requeue: true}, nil
}

func (r *TargetReconciler) handleWaitingApproval(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, rt *kaprov1alpha2.Target) (ctrl.Result, error) {
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
		r.transitionTo(ctx, promotionrun, target, kaprov1alpha2.TargetPhaseApplying)
		return ctrl.Result{Requeue: true}, nil
	}

	r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "WaitingApproval",
		"Waiting for Approval CR for %s", target.Target)
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func (r *TargetReconciler) sendApprovalNotification(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState) {
	_ = ctx
	_ = promotionrun
	target.ApprovalSentAt = time.Now().UTC().Format(time.RFC3339)
}

func (r *TargetReconciler) handleApplying(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	desiredVersions := targetDesiredVersions(target)
	if len(desiredVersions) == 0 {
		r.failTarget(ctx, promotionrun, target, "target has no desired versions to apply")
		return ctrl.Result{}, nil
	}

	var mc kaprov1alpha2.Cluster
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
			target.Phase = kaprov1alpha2.TargetPhaseConverged
			target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			return ctrl.Result{}, nil
		}
	}

	if mc.Status.Phase == kaprov1alpha2.ClusterPhaseConverged &&
		currentVersion == target.Version && len(desiredVersions) == 1 {
		l.Info("cluster converged", "cluster", target.Target, "version", target.Version)
		r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "Applied",
			"Version %s applied to %s", target.Version, target.Target)
		target.Phase = kaprov1alpha2.TargetPhaseConverged
		target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		return ctrl.Result{}, nil
	}

	if mc.Status.Phase == kaprov1alpha2.ClusterPhaseFailed {
		r.failTarget(ctx, promotionrun, target,
			fmt.Sprintf("cluster %s reported Failed phase", target.Target))
		return ctrl.Result{}, nil
	}

	// Phase=Unreachable means the ClusterHeartbeatReconciler has crossed
	// the per-cluster ConsecutiveFailureThreshold. Defer (do not fail) — a
	// transient network outage shouldn't trash an in-flight promotion. The
	// reconciler will flip Phase back as soon as a fresh heartbeat lands; the
	// requeue here is a belt-and-braces re-check in case watch events miss.
	if mc.Status.Phase == kaprov1alpha2.ClusterPhaseUnreachable {
		if r.Recorder != nil {
			msg := fmt.Sprintf("cluster %s is Unreachable; deferring", target.Target)
			if ready := apimeta.FindStatusCondition(mc.Status.Conditions, kaprov1alpha2.ConditionTypeReady); ready != nil && ready.Message != "" {
				msg = fmt.Sprintf("cluster %s is Unreachable: %s; deferring", target.Target, ready.Message)
			}
			r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "ClusterUnreachable", "%s", msg)
		}
		l.Info("cluster Unreachable; deferring", "cluster", target.Target)
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	// Warn when the cluster reports Converged but CurrentVersions is absent or stale —
	// this indicates the cluster-controller has not yet populated the version map.
	if mc.Status.Phase == kaprov1alpha2.ClusterPhaseConverged && currentVersion != target.Version {
		l.Info("cluster Converged but CurrentVersions not yet updated — waiting for cluster-controller",
			"cluster", target.Target, "currentVersion", currentVersion, "wantVersion", target.Version)
	} else {
		l.Info("waiting for convergence",
			"cluster", target.Target, "clusterPhase", mc.Status.Phase,
			"currentVersion", currentVersion, "wantVersion", target.Version)
	}
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func capturePreviousVersions(target *kaprov1alpha2.TargetExecutionState, mc *kaprov1alpha2.Cluster, desiredVersions map[string]string) {
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

func validateTargetTopology(mc *kaprov1alpha2.Cluster, desiredVersions map[string]string) error {
	if len(desiredVersions) <= 1 || mc.Spec.Delivery.Mode != kaprov1alpha2.DeliveryModePull || mc.Spec.Delivery.BackendRef != "flux" {
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
//
// D3-PR2: validates the transition against the FSM table's declared
// AllowedNext metadata. An undeclared transition emits a Warning event +
// the kapro_fsm_unexpected_transitions_total metric counter and then
// proceeds (observability, not enforcement). Crashing on a state-graph
// drift in production would be strictly worse than letting the transition
// through with a loud alert; the graph is documentation and a violation
// means the documentation is stale, not that the rollout is unsafe.
func (r *TargetReconciler) transitionTo(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, phase kaprov1alpha2.TargetPhase) {
	prevPhase := target.Phase

	r.ensureFSM()
	if err := r.fsmMachine.ValidateTransition(prevPhase, phase); err != nil {
		r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "FSMUnexpectedTransition",
			"[%s/%s] %s", target.Stage, target.Target, err.Error())
		kaprometrics.FSMUnexpectedTransitions.WithLabelValues(string(prevPhase), string(phase)).Inc()
	}

	target.Phase = phase
	if phase == kaprov1alpha2.TargetPhaseSoaking && target.StartedAt == "" {
		target.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if phase == kaprov1alpha2.TargetPhaseApplying {
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
func phaseTransitionMessage(from kaprov1alpha2.TargetPhase, to kaprov1alpha2.TargetPhase, target *kaprov1alpha2.TargetExecutionState) string {
	switch to {
	case kaprov1alpha2.TargetPhasePending:
		return fmt.Sprintf("queued for delivery of %s", target.Version)
	case kaprov1alpha2.TargetPhaseVerification:
		return fmt.Sprintf("verifying artifact signature for %s", target.Version)
	case kaprov1alpha2.TargetPhaseHealthCheck:
		return "checking pre-deployment cluster health"
	case kaprov1alpha2.TargetPhaseSoaking:
		if target.Gate != nil && target.Gate.Gate.SoakTime != "" {
			return fmt.Sprintf("soak timer started (%s)", target.Gate.Gate.SoakTime)
		}
		return "soak timer started"
	case kaprov1alpha2.TargetPhaseMetricsCheck:
		return "evaluating metrics gates"
	case kaprov1alpha2.TargetPhaseWaitingApproval:
		return "waiting for human approval"
	case kaprov1alpha2.TargetPhaseApplying:
		return fmt.Sprintf("applying version %s to cluster", target.Version)
	case kaprov1alpha2.TargetPhaseConverged:
		return fmt.Sprintf("cluster converged on %s", target.Version)
	case kaprov1alpha2.TargetPhaseFailed:
		if target.Message != "" {
			return target.Message
		}
		return "target failed"
	case kaprov1alpha2.TargetPhaseSkipped:
		return "target skipped (onFailure=continue)"
	default:
		return string(to)
	}
}

// failTarget records a failure and applies the onFailure policy.
func (r *TargetReconciler) failTarget(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState, msg string) {
	target.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	target.Message = msg

	onFailure := "halt"
	if target.Gate != nil && target.Gate.OnFailure != "" {
		onFailure = target.Gate.OnFailure
	}

	if onFailure == "continue" {
		target.Phase = kaprov1alpha2.TargetPhaseSkipped
		r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "TargetSkipped",
			"[%s/%s] skipped (onFailure=continue): %s", target.Stage, target.Target, msg)
		if rt := promotionTargetFromContext(ctx); rt != nil {
			r.Recorder.Eventf(rt, corev1.EventTypeWarning, "Skipped", "skipped: %s", msg)
		}
		return
	}

	target.Phase = kaprov1alpha2.TargetPhaseFailed
	r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "TargetFailed",
		"[%s/%s] failed: %s", target.Stage, target.Target, msg)
	if rt := promotionTargetFromContext(ctx); rt != nil {
		r.Recorder.Eventf(rt, corev1.EventTypeWarning, "Failed", "failed: %s", msg)
	}

	// Rollback is triggered by the parent PromotionRunReconciler when it aggregates
	// child statuses and detects a Failed target with onFailure=rollback.
	// The child never creates sibling PromotionTarget objects.
}

func (r *TargetReconciler) notifyPersistedTransitions(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, previous, current *kaprov1alpha2.TargetExecutionState) {
	if r.Notifier == nil {
		return
	}

	prevPhase := previous.Phase
	currPhase := current.Phase
	if prevPhase != currPhase && currPhase != kaprov1alpha2.TargetPhaseWaitingApproval {
		r.Notifier.Notify(ctx, notification.Event{
			Type:         eventTypeForPhase(currPhase),
			Phase:        string(currPhase),
			Version:      current.Version,
			Target:       current.Target,
			PromotionRun: promotionrun.Name,
			Plan:         current.PlanRef,
			Stage:        current.Stage,
			Message:      current.Message,
			IsFailure:    currPhase == kaprov1alpha2.TargetPhaseFailed,
		}, notificationPolicyFrom(current.Gate))
	}

	if previous.ApprovalSentAt == "" && current.ApprovalSentAt != "" {
		r.notifyApprovalRequest(ctx, promotionrun, current)
	}
}

func (r *TargetReconciler) notifyApprovalRequest(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, target *kaprov1alpha2.TargetExecutionState) {
	var approveURL, rejectURL string
	if len(r.ApprovalSecret) > 0 && r.ExternalURL != "" {
		var err error
		approveURL, rejectURL, err = buildApprovalURLs(r.ExternalURL, r.ApprovalSecret, promotionrun, target)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to build approval URLs")
		}
	}

	r.Notifier.Notify(ctx, notification.Event{
		Type:         notification.EventApprovalRequired,
		Phase:        string(kaprov1alpha2.TargetPhaseWaitingApproval),
		Version:      target.Version,
		Target:       target.Target,
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Message:      "Approval required to proceed",
		ApproveURL:   approveURL,
		RejectURL:    rejectURL,
	}, notificationPolicyFrom(target.Gate))
}

// --- SetupWithManager & watch mappers ---

func (r *TargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	forPredicates := []predicate.Predicate{promotionTargetPredicates()}
	if r.ShardPredicate != nil {
		forPredicates = append(forPredicates, r.ShardPredicate)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 5*time.Minute),
		}).
		For(&kaprov1alpha2.Target{}, builder.WithPredicates(forPredicates...)).
		Watches(
			&kaprov1alpha2.Approval{},
			handler.EnqueueRequestsFromMapFunc(promotionTargetsForApproval),
		).
		Watches(
			&kaprov1alpha2.Cluster{},
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
			oldRT, okOld := e.ObjectOld.(*kaprov1alpha2.Target)
			newRT, okNew := e.ObjectNew.(*kaprov1alpha2.Target)
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
	approval, ok := obj.(*kaprov1alpha2.Approval)
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
			oldMC, okOld := e.ObjectOld.(*kaprov1alpha2.Cluster)
			newMC, okNew := e.ObjectNew.(*kaprov1alpha2.Cluster)
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

func (r *TargetReconciler) promotionTargetsForFleetCluster(ctx context.Context, obj client.Object) []ctrl.Request {
	mc, ok := obj.(*kaprov1alpha2.Cluster)
	if !ok {
		return nil
	}
	var targetList kaprov1alpha2.TargetList
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

func (r *TargetReconciler) promotionTargetsForHeartbeatLease(ctx context.Context, obj client.Object) []ctrl.Request {
	if !isHeartbeatLeaseObject(obj) {
		return nil
	}
	clusterName := strings.TrimPrefix(obj.GetName(), heartbeatLeasePrefix)
	var targetList kaprov1alpha2.TargetList
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

func (r *TargetReconciler) updatePromotionTargetStatusContract(rt *kaprov1alpha2.Target) {
	rt.Status.ObservedGeneration = rt.Generation

	phase := rt.Status.Phase
	switch phase {
	case kaprov1alpha2.TargetPhaseConverged:
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionTrue, "Converged", "target converged")
		r.setPromotionTargetCondition(rt, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionFalse, "Converged", "target converged")
		apimeta.RemoveStatusCondition(&rt.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	case kaprov1alpha2.TargetPhaseFailed:
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionFalse, "Failed", rt.Status.Message)
		r.setPromotionTargetCondition(rt, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionFalse, "Failed", "target failed")
		r.setPromotionTargetCondition(rt, kaprov1alpha2.ConditionTypeStalled, metav1.ConditionTrue, "Failed", rt.Status.Message)
	case kaprov1alpha2.TargetPhaseSkipped:
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionFalse, "Skipped", rt.Status.Message)
		r.setPromotionTargetCondition(rt, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionFalse, "Skipped", "target skipped")
		apimeta.RemoveStatusCondition(&rt.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	default:
		message := "target is progressing"
		if rt.Status.Message != "" {
			message = rt.Status.Message
		}
		r.setPromotionTargetCondition(rt, "Ready", metav1.ConditionFalse, string(phase), message)
		r.setPromotionTargetCondition(rt, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionTrue, string(phase), message)
		apimeta.RemoveStatusCondition(&rt.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	}
}

func (r *TargetReconciler) syncPromotionTargetPhaseLabel(ctx context.Context, rt *kaprov1alpha2.Target) error {
	phase := rt.Status.Phase
	if phase == "" {
		phase = kaprov1alpha2.TargetPhasePending
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

func (r *TargetReconciler) setPromotionTargetCondition(rt *kaprov1alpha2.Target, conditionType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: rt.Generation,
		LastTransitionTime: metav1.Now(),
	})
}
