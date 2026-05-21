package controller

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	internalgate "kapro.io/kapro/internal/gate"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/promotion/fsm"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/planner"
)

// promotionrunFinalizer uses the canonical constant from the API package
// to avoid mismatch between controller and external tooling.
const promotionrunFinalizer = kaprov1alpha2.PromotionRunFinalizer

const (
	maxGateRunsPerTarget            = 16
	maxGateResultsPerGateRun        = 16
	maxPromotionRunReadyMessageSize = 256
	maxPlannerResultsPerStage       = 32
)

// PromotionRunReconciler is the main brain of Kapro.
// It drives two DAG levels:
//
//  1. Plan DAG — PromotionRun.spec.promotionplans[].dependsOn orders which promotionplans
//     run in sequence (or parallel when no deps). Useful when the same fleet is
//     partitioned into logical "apps" that must be released in a fixed order.
//
//  2. Stage DAG — Plan.spec.stages[].dependsOn orders stages within each
//     promotionplan (pilot → canary → global). Each stage expands to N Syncs — one
//     per matching target — which run in parallel.
//
// State machine:
//
//	Pending → Progressing → Complete | Failed
type PromotionRunReconciler struct {
	client.Client
	Recorder         record.EventRecorder
	Scheme           *runtime.Scheme
	ActuatorRegistry *actuator.Registry
	Notifier         notification.Notifier
	ApprovalSecret   []byte
	ExternalURL      string

	// GateRegistry resolves every gate by name — both FSM-phase gates
	// ("soak", "metrics", "approval", "verification") and template-dispatch
	// gates (GateTemplate.spec.type). Never nil in production.
	GateRegistry *gate.Registry

	// ShardPredicate optionally filters objects by shard label for horizontal scaling.
	// When nil, all objects are processed.
	ShardPredicate predicate.Predicate

	// Planner orders and filters target clusters using scheduler-style extension phases.
	// Nil means the default empty planner.
	Planner *planner.Framework

	// StagePublisher emits kapro.io/promotion.wave.* and stage.* events
	// to the operator-level CloudEvents sink on Plan / Stage
	// transitions. Nil-safe — when unset, no fleet-narrative events fire.
	StagePublisher StageEventPublisher

	// runFsmMachine is the declarative dispatch table for the PromotionRun
	// phase FSM (D2-PR1). Built lazily via ensureRunFSM so direct
	// reconciler construction in tests works without SetupWithManager.
	runFsmOnce    sync.Once
	runFsmMachine *fsm.Machine[kaprov1alpha2.PromotionRunPhase, *runFsmEnv]
}

// runFsmEnv is the per-Reconcile state bag the PromotionRun FSM hands to
// each phase handler. Held by pointer so handlers see the same
// promotionrun value the reconciler holds across the step's lifetime.
type runFsmEnv struct {
	promotionrun *kaprov1alpha2.PromotionRun
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=fleetclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=fleetclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionplans,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

func (r *PromotionRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	resultLabel := "success"
	defer func() {
		kaprometrics.ControllerReconciles.WithLabelValues("promotionrun", resultLabel).Inc()
		kaprometrics.ControllerReconcileDuration.WithLabelValues("promotionrun").Observe(time.Since(start).Seconds())
	}()

	log := log.FromContext(ctx)

	var promotionrun kaprov1alpha2.PromotionRun
	if err := r.Get(ctx, req.NamespacedName, &promotionrun); err != nil {
		resultLabel = "error"
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling PromotionRun",
		"name", promotionrun.Name,
		"phase", promotionrun.Status.Phase,
		"version", promotionrun.Spec.Version,
	)

	if !promotionrun.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &promotionrun)
	}

	if !controllerutil.ContainsFinalizer(&promotionrun, promotionrunFinalizer) {
		patch := client.MergeFrom(promotionrun.DeepCopy())
		controllerutil.AddFinalizer(&promotionrun, promotionrunFinalizer)
		if err := r.Patch(ctx, &promotionrun, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if promotionrun.Spec.Suspended {
		log.Info("PromotionRun is suspended — skipping FSM advancement")
		patch := client.MergeFrom(promotionrun.DeepCopy())
		r.setPromotionRunReadyCondition(&promotionrun, metav1.ConditionFalse, "Suspended", "PromotionRun is suspended")
		r.setReconcilingCondition(&promotionrun, metav1.ConditionFalse, "Suspended", "PromotionRun is suspended")
		apimeta.RemoveStatusCondition(&promotionrun.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
		promotionrun.Status.ObservedGeneration = promotionrun.Generation
		if patchErr := r.patchPromotionRunStatus(ctx, &promotionrun, patch); patchErr != nil {
			resultLabel = "error"
			return ctrl.Result{}, fmt.Errorf("patch suspended conditions: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	// Dispatch via the FSM (D2-PR1). Replaces the legacy phase switch;
	// behaviour unchanged. Phase mutation continues to happen as a side
	// effect inside the handlers (handlePending sets Progressing,
	// handleProgressing sets Complete or Failed via patchPromotionRunStatus).
	r.ensureRunFSM()
	return r.runFsmMachine.Step(ctx, promotionrun.Status.Phase, &runFsmEnv{promotionrun: &promotionrun})
}

// ensureRunFSM builds the PromotionRun phase dispatch table the first
// time it is called. Lazy init keeps unit tests that construct
// PromotionRunReconciler directly (without SetupWithManager) working —
// same pattern as PromotionTarget's ensureFSM (D3-PR1).
func (r *PromotionRunReconciler) ensureRunFSM() {
	r.runFsmOnce.Do(func() {
		r.runFsmMachine = r.buildRunFSM()
	})
}

// buildRunFSM constructs the PromotionRun phase dispatch table.
//
// Declared phase graph (D2-PR2 — AllowedNext on every Register;
// promotionrun_fsm_graph_test.go locks this against the comment block):
//
//	""              → Progressing  (via handlePending, which sets the phase)
//	Pending         → Progressing, Failed
//	Progressing     → Complete, Failed
//	Failed          → Failed       (sticky during rollback; ValidateTransition
//	                                treats from==to as a no-op so the loop
//	                                doesn't fire spurious warnings)
//	Complete        → terminal (no handler, RegisterTerminal)
//
// All non-terminal phases keep Failed in AllowedNext because
// handleTimeout / setStalledCondition paths can flip the phase from any
// non-terminal state when the global PromotionRun timeout fires.
func (r *PromotionRunReconciler) buildRunFSM() *fsm.Machine[kaprov1alpha2.PromotionRunPhase, *runFsmEnv] {
	m := fsm.New[kaprov1alpha2.PromotionRunPhase, *runFsmEnv]()
	must(m.RegisterInitial(func(ctx context.Context, e *runFsmEnv) (ctrl.Result, error) {
		return r.handlePending(ctx, e.promotionrun)
	}, kaprov1alpha2.PromotionRunPhaseProgressing))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.PromotionRunPhase, *runFsmEnv]{
		Phase: kaprov1alpha2.PromotionRunPhasePending,
		AllowedNext: []kaprov1alpha2.PromotionRunPhase{
			kaprov1alpha2.PromotionRunPhaseProgressing,
			kaprov1alpha2.PromotionRunPhaseFailed,
		},
		Handler: func(ctx context.Context, e *runFsmEnv) (ctrl.Result, error) {
			return r.handlePending(ctx, e.promotionrun)
		},
	}))
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.PromotionRunPhase, *runFsmEnv]{
		Phase: kaprov1alpha2.PromotionRunPhaseProgressing,
		AllowedNext: []kaprov1alpha2.PromotionRunPhase{
			kaprov1alpha2.PromotionRunPhaseComplete,
			kaprov1alpha2.PromotionRunPhaseFailed,
		},
		Handler: func(ctx context.Context, e *runFsmEnv) (ctrl.Result, error) {
			return r.handleProgressing(ctx, e.promotionrun)
		},
	}))
	// Failed is NOT terminal — when rollback targets are active the
	// reconciler keeps driving handleFailed until they converge. The
	// guard moves into this closure so the legacy
	// "if hasActiveRollbackTargets" pre-check is encoded at the
	// dispatch site rather than the call site. AllowedNext is empty
	// (Failed is sticky); ValidateTransition treats from==to as
	// always-allowed so the rollback loop doesn't fire warnings.
	must(m.RegisterTransition(fsm.Transition[kaprov1alpha2.PromotionRunPhase, *runFsmEnv]{
		Phase: kaprov1alpha2.PromotionRunPhaseFailed,
		Handler: func(ctx context.Context, e *runFsmEnv) (ctrl.Result, error) {
			if !r.hasActiveRollbackTargets(e.promotionrun) {
				return ctrl.Result{}, nil
			}
			return r.handleFailed(ctx, e.promotionrun)
		},
	}))
	must(m.RegisterTerminal(kaprov1alpha2.PromotionRunPhaseComplete))
	must(m.RegisterTerminal(kaprov1alpha2.PromotionRunPhaseSuperseded))
	return m
}

// setRunPhase mutates promotionrun.Status.Phase after validating the
// transition against the declared FSM graph (D2-PR2). Same observability-
// not-enforcement stance as PromotionTarget's transitionTo: undeclared
// transitions emit a Warning event + bump
// kapro_fsm_unexpected_transitions_total{from,to} and proceed. Crashing
// the reconciler on a graph-doc drift would be strictly worse than
// alerting; the FSM graph is documentation and a violation means the
// docs are stale, not that the rollout is unsafe.
//
// Callers MUST funnel every Phase assignment through here so the
// validation hook is single-source — direct mutations bypass the check
// and silently drift the graph. The pkg/golangci-lint exhaustive linter
// is the long-term enforcement; for now the call-site discipline is
// covered by code review and the graph-adjacency test.
func (r *PromotionRunReconciler) setRunPhase(promotionrun *kaprov1alpha2.PromotionRun, newPhase kaprov1alpha2.PromotionRunPhase) {
	prevPhase := promotionrun.Status.Phase
	r.ensureRunFSM()
	if err := r.runFsmMachine.ValidateTransition(prevPhase, newPhase); err != nil {
		r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "FSMUnexpectedTransition",
			"[promotionrun/%s] %s", promotionrun.Name, err.Error())
		kaprometrics.FSMUnexpectedTransitions.WithLabelValues(string(prevPhase), string(newPhase)).Inc()
	}
	promotionrun.Status.Phase = newPhase
}

func (r *PromotionRunReconciler) patchPromotionRunStatus(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, patch client.Patch) error {
	if err := r.Status().Patch(ctx, promotionrun, patch); err != nil {
		kaprometrics.StatusWrites.WithLabelValues("promotionrun", "error").Inc()
		return err
	}
	kaprometrics.StatusWrites.WithLabelValues("promotionrun", "success").Inc()
	return nil
}

// handlePending validates the promotionrun revisions and transitions to Progressing.
func (r *PromotionRunReconciler) handlePending(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	desiredVersions := promotionrunDesiredVersionsFromSpec(promotionrun)
	if len(desiredVersions) == 0 {
		patch := client.MergeFrom(promotionrun.DeepCopy())
		r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionFalse, "NoVersion", "spec.version or spec.versions is required")
		r.setStalledCondition(promotionrun, "NoVersion", "spec.version or spec.versions is required")
		promotionrun.Status.ObservedGeneration = promotionrun.Generation
		if err := r.patchPromotionRunStatus(ctx, promotionrun, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch stalled: %w", err)
		}
		return ctrl.Result{}, nil
	}

	resolvedVersion := promotionrunPrimaryVersion(promotionrun, desiredVersions)
	log.Info("version resolved", "version", resolvedVersion, "versions", len(desiredVersions))

	progress := make([]kaprov1alpha2.PromotionPlanProgress, 0, len(promotionrun.Spec.PromotionPlans))
	for _, ref := range promotionrun.Spec.PromotionPlans {
		progress = append(progress, kaprov1alpha2.PromotionPlanProgress{
			Name: ref.Name, Plan: ref.Plan, Phase: "Pending",
		})
	}

	patch := client.MergeFrom(promotionrun.DeepCopy())
	r.setRunPhase(promotionrun, kaprov1alpha2.PromotionRunPhaseProgressing)
	promotionrun.Status.ResolvedVersion = resolvedVersion
	promotionrun.Status.PromotionPlanProgress = progress
	promotionrun.Status.ObservedGeneration = promotionrun.Generation
	promotionrun.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionFalse, "Progressing", "promotionrun is advancing")
	r.clearStalledCondition(promotionrun)
	r.setReconcilingCondition(promotionrun, metav1.ConditionTrue, "Progressing", "advancing through promotionplan DAG")
	promotionplanNames := make([]string, 0, len(promotionrun.Spec.PromotionPlans))
	for _, p := range promotionrun.Spec.PromotionPlans {
		promotionplanNames = append(promotionplanNames, p.Plan)
	}
	r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "Started",
		"promotionrun %s started: version=%s promotionplans=%v", promotionrun.Name, resolvedVersion, promotionplanNames)
	if err := r.patchPromotionRunStatus(ctx, promotionrun, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch PromotionRun phase: %w", err)
	}
	r.notifyPromotionRunEvent(ctx, promotionrun, notification.EventPromotionRunStarted, "promotionrun started")
	return ctrl.Result{Requeue: true}, nil
}

// handleProgressing drives the two-level DAG:
//
//	Plan DAG (outer) → Stage DAG (inner) → Targets per Stage
//
// For each promotionplan whose dependencies are complete, we walk its stages in
// dependsOn order. For each eligible stage we list matching Targets,
// upsert an TargetStatus entry in promotionrun.Status.Targets, and
// observe current phases. advanceAllTargets then moves each non-terminal
// env one FSM step forward. A single Status().Patch() persists everything.
func (r *PromotionRunReconciler) handleProgressing(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check global PromotionRun timeout — fail the entire PromotionRun if it exceeded.
	if promotionrun.Spec.Timeout != "" && promotionrun.Status.StartedAt != "" {
		timeout, err := time.ParseDuration(promotionrun.Spec.Timeout)
		if err == nil {
			startedAt, parseErr := time.Parse(time.RFC3339, promotionrun.Status.StartedAt)
			if parseErr == nil && time.Since(startedAt) > timeout {
				log.Info("PromotionRun exceeded timeout", "timeout", promotionrun.Spec.Timeout,
					"startedAt", promotionrun.Status.StartedAt, "elapsed", time.Since(startedAt))
				return r.handleTimeout(ctx, promotionrun)
			}
		}
	}

	// CRITICAL: take snapshot BEFORE any mutations to promotionrun.Status.
	// advanceAllTargets, upsertTarget, cancelPendingStageTargets, and
	// triggerRollbackTargets all mutate promotionrun.Status in-place; one patch at the
	// bottom persists the full diff.
	patch := client.MergeFrom(promotionrun.DeepCopy())

	if err := r.loadPromotionTargets(ctx, promotionrun); err != nil {
		return ctrl.Result{}, fmt.Errorf("load promotion targets: %w", err)
	}

	// Build promotionplan phase map from current PromotionPlanProgress.
	promotionplanPhase := make(map[string]string, len(promotionrun.Status.PromotionPlanProgress))
	promotionplanProgress := make(map[string]kaprov1alpha2.PromotionPlanProgress, len(promotionrun.Status.PromotionPlanProgress))
	for _, p := range promotionrun.Status.PromotionPlanProgress {
		promotionplanPhase[p.Name] = p.Phase
		promotionplanProgress[p.Name] = p
	}

	// Track updated progress (written back once at the end).
	updatedPromotionPlans := make([]kaprov1alpha2.PromotionPlanProgress, 0, len(promotionrun.Spec.PromotionPlans))
	allPromotionPlansComplete := true
	var failureMsg string
	var pendingCancels []cancelRequest
	var nextRequeue time.Duration

	for _, promotionplanRef := range promotionrun.Spec.PromotionPlans {
		currentPhase := promotionplanPhase[promotionplanRef.Name]

		if currentPhase == "Complete" {
			previous := promotionplanProgress[promotionplanRef.Name]
			updatedPromotionPlans = append(updatedPromotionPlans, kaprov1alpha2.PromotionPlanProgress{
				Name:               promotionplanRef.Name,
				Plan:      promotionplanRef.Plan,
				ObservedGeneration: previous.ObservedGeneration,
				Phase:              "Complete",
				ActiveStage:        previous.ActiveStage,
				StageProgress:      previous.StageProgress,
			})
			continue
		}
		if currentPhase == "Failed" {
			allPromotionPlansComplete = false
			previous := promotionplanProgress[promotionplanRef.Name]
			updatedPromotionPlans = append(updatedPromotionPlans, kaprov1alpha2.PromotionPlanProgress{
				Name:               promotionplanRef.Name,
				Plan:      promotionplanRef.Plan,
				ObservedGeneration: previous.ObservedGeneration,
				Phase:              "Failed",
				ActiveStage:        previous.ActiveStage,
				StageProgress:      previous.StageProgress,
			})
			continue
		}

		// Check promotionplan-level dependencies.
		depsComplete := true
		for _, dep := range promotionplanRef.DependsOn {
			if promotionplanPhase[dep] != "Complete" {
				depsComplete = false
				break
			}
		}
		if !depsComplete {
			allPromotionPlansComplete = false
			previous := promotionplanProgress[promotionplanRef.Name]
			updatedPromotionPlans = append(updatedPromotionPlans, kaprov1alpha2.PromotionPlanProgress{
				Name:               promotionplanRef.Name,
				Plan:      promotionplanRef.Plan,
				ObservedGeneration: previous.ObservedGeneration,
				Phase:              "Pending",
				ActiveStage:        previous.ActiveStage,
				StageProgress:      previous.StageProgress,
			})
			continue
		}

		// Plan is eligible — resolve its stage DAG.
		var promotionplan kaprov1alpha2.Plan
		if err := r.Get(ctx, client.ObjectKey{Name: promotionplanRef.Plan}, &promotionplan); err != nil {
			return ctrl.Result{}, fmt.Errorf("promotionplan %s not found: %w", promotionplanRef.Plan, err)
		}
		previous := promotionplanProgress[promotionplanRef.Name]
		if previous.ObservedGeneration != 0 && previous.ObservedGeneration != promotionplan.Generation {
			msg := fmt.Sprintf("promotionplan %s changed during promotionrun: observed generation %d, current generation %d", promotionplan.Name, previous.ObservedGeneration, promotionplan.Generation)
			r.setRunPhase(promotionrun, kaprov1alpha2.PromotionRunPhaseFailed)
			promotionrun.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionFalse, "PromotionPlanChanged", msg)
			r.setStalledCondition(promotionrun, "PromotionPlanChanged", msg)
			if err := r.patchPromotionRunStatus(ctx, promotionrun, patch); err != nil {
				return ctrl.Result{}, fmt.Errorf("patch PromotionRun status (promotionplan changed): %w", err)
			}
			r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "PromotionPlanChanged", msg)
			r.notifyPromotionRunEvent(ctx, promotionrun, notification.EventPromotionRunFailed, msg)
			return ctrl.Result{}, nil
		}

		stageProgress, promotionplanDone, promotionplanFailed, requeueAfter, cancels, err := r.reconcilePromotionPlanStages(
			ctx, promotionrun, promotionplanRef.Name, &promotionplan,
		)
		if err != nil {
			return ctrl.Result{}, err
		}
		pendingCancels = append(pendingCancels, cancels...)
		if requeueAfter > 0 && (nextRequeue == 0 || requeueAfter < nextRequeue) {
			nextRequeue = requeueAfter
		}

		newPhase := "Progressing"
		if promotionplanFailed {
			newPhase = "Failed"
			allPromotionPlansComplete = false
			failureMsg = fmt.Sprintf("promotionplan %s (%s) failed", promotionplanRef.Name, promotionplanRef.Plan)
		} else if promotionplanDone {
			newPhase = "Complete"
			log.Info("promotionplan complete", "promotionplanRef", promotionplanRef.Name)
		} else {
			allPromotionPlansComplete = false
		}

		// kapro.io/promotion.wave.* fleet-narrative events. Guard against
		// re-emission by comparing against the previously observed phase
		// recorded in promotionrun.Status.PromotionPlanProgress.
		prevPlanPhase := previousPromotionPlanPhase(promotionrun, promotionplanRef.Name)
		if newPhase == "Progressing" && prevPlanPhase != "Progressing" && prevPlanPhase != "Complete" && prevPlanPhase != "Failed" {
			r.publishWaveEvent(ctx, promotionrun, promotionplanRef.Name, "entered", newPhase,
				fmt.Sprintf("Wave %s entered", promotionplanRef.Name))
		}
		if (newPhase == "Complete" || newPhase == "Failed") && prevPlanPhase != newPhase {
			r.publishWaveEvent(ctx, promotionrun, promotionplanRef.Name, "completed", newPhase,
				fmt.Sprintf("Wave %s %s", promotionplanRef.Name, strings.ToLower(newPhase)))
		}

		// Derive active stage for quick "where are we?" in k9s.
		activeStage := ""
		for i := len(stageProgress) - 1; i >= 0; i-- {
			if stageProgress[i].Phase == "Progressing" || stageProgress[i].Phase == "Failed" {
				activeStage = stageProgress[i].Name
				break
			}
			if stageProgress[i].Phase == "Complete" && activeStage == "" {
				activeStage = stageProgress[i].Name
			}
		}

		updatedPromotionPlans = append(updatedPromotionPlans, kaprov1alpha2.PromotionPlanProgress{
			Name:               promotionplanRef.Name,
			Plan:      promotionplanRef.Plan,
			ObservedGeneration: promotionplan.Generation,
			Phase:              newPhase,
			ActiveStage:        activeStage,
			StageProgress:      stageProgress,
		})

		if promotionplanFailed {
			// Fail fast: mark promotionrun failed using the outer patch (which already
			// includes any target mutations from upsertTarget/cancelPendingStageTargets).
			r.setRunPhase(promotionrun, kaprov1alpha2.PromotionRunPhaseFailed)
			promotionrun.Status.ObservedGeneration = promotionrun.Generation
			promotionrun.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			promotionrun.Status.PromotionPlanProgress = updatedPromotionPlans
			promotionrun.Status.Report = r.computeReport(promotionrun)
			r.normalizePromotionRunStatus(promotionrun)
			if err := r.persistPromotionTargets(ctx, promotionrun); err != nil {
				return ctrl.Result{}, fmt.Errorf("persist promotion targets: %w", err)
			}
			// Apply deferred cancellations AFTER persistPromotionTargets so the
			// cache-based spec writes don't overwrite spec.cancelled.
			for _, c := range pendingCancels {
				r.cancelPendingStageTargets(ctx, promotionrun, c.promotionplanRef, c.stage)
			}
			hasRollbacks := r.hasActiveRollbackTargets(promotionrun)
			promotionrun.Status.Targets = nil
			r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionFalse, "SubResourceFailed", failureMsg)
			r.setStalledCondition(promotionrun, "SubResourceFailed", failureMsg)
			if hasRollbacks {
				r.setReconcilingCondition(promotionrun, metav1.ConditionTrue, "RollbackInProgress", "promotionrun failed and rollback targets are still progressing")
			} else {
				r.setReconcilingCondition(promotionrun, metav1.ConditionFalse, "SubResourceFailed", failureMsg)
			}
			r.Recorder.Event(promotionrun, corev1.EventTypeWarning, "Failed", failureMsg)
			if patchErr := r.patchPromotionRunStatus(ctx, promotionrun, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch PromotionRun status on failure: %w", patchErr)
			}
			r.notifyPromotionRunEvent(ctx, promotionrun, notification.EventPromotionRunFailed, failureMsg)
			if hasRollbacks {
				return ctrl.Result{Requeue: true}, nil
			}
			r.clearActivePromotionRun(ctx, promotionrun)
			return ctrl.Result{}, nil
		}
	}

	// Child PromotionTarget reconciles advance per-target FSM state; the PromotionRun
	// reconcile only persists orchestration-side mutations (upserts, cancels,
	// rollback target creation) and aggregates child state.
	promotionrun.Status.PromotionPlanProgress = updatedPromotionPlans
	promotionrun.Status.ObservedGeneration = promotionrun.Generation
	// Set terminal phase fields BEFORE computeReport so the report captures the
	// correct Phase and CompletedAt (B50: previously set after targets were cleared).
	if allPromotionPlansComplete {
		r.appendAuditEntry(ctx, promotionrun)
		r.setRunPhase(promotionrun, kaprov1alpha2.PromotionRunPhaseComplete)
		promotionrun.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// Compute report while targets are still in memory; normalization and
	// persistence happen after so the report reflects the full target set.
	promotionrun.Status.Report = r.computeReport(promotionrun)
	r.normalizePromotionRunStatus(promotionrun)
	if err := r.persistPromotionTargets(ctx, promotionrun); err != nil {
		return ctrl.Result{}, fmt.Errorf("persist promotion targets: %w", err)
	}
	for _, c := range pendingCancels {
		r.cancelPendingStageTargets(ctx, promotionrun, c.promotionplanRef, c.stage)
	}
	promotionrun.Status.Targets = nil

	if allPromotionPlansComplete {
		r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionTrue, "Complete", "all promotionplans complete")
		r.clearStalledCondition(promotionrun)
		r.setReconcilingCondition(promotionrun, metav1.ConditionFalse, "Complete", "all promotionplans complete")
		duration := ""
		if promotionrun.Status.StartedAt != "" {
			if startT, err := time.Parse(time.RFC3339, promotionrun.Status.StartedAt); err == nil {
				duration = time.Since(startT).Truncate(time.Second).String()
			}
		}
		r.Recorder.Eventf(promotionrun, corev1.EventTypeNormal, "Complete",
			"all promotionplans complete: version=%s targets=%d duration=%s",
			promotionrun.Spec.Version, promotionrun.Status.Report.TotalTargets, duration)
	} else {
		r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionFalse, "Progressing", promotionrunProgressSummary(promotionrun))
		r.clearStalledCondition(promotionrun)
		r.setReconcilingCondition(promotionrun, metav1.ConditionTrue, "Progressing", "promotionrun is advancing through promotionplan DAG")
	}
	if err := r.patchPromotionRunStatus(ctx, promotionrun, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch PromotionRun status: %w", err)
	}

	if allPromotionPlansComplete {
		r.notifyPromotionRunEvent(ctx, promotionrun, notification.EventPromotionRunCompleted, "promotionrun completed")
		r.clearActivePromotionRun(ctx, promotionrun)
		annPatch := client.MergeFrom(promotionrun.DeepCopy())
		if promotionrun.Annotations == nil {
			promotionrun.Annotations = make(map[string]string)
		}
		promotionrun.Annotations["kapro.io/previous-version"] = promotionrun.Status.ResolvedVersion
		if annErr := r.Patch(ctx, promotionrun, annPatch); annErr != nil {
			log.Error(annErr, "failed to annotate previous-version on PromotionRun")
		}
		log.Info("PromotionRun complete", "name", promotionrun.Name)
		if nextRequeue > 0 {
			return ctrl.Result{RequeueAfter: nextRequeue}, nil
		}
		return ctrl.Result{}, nil
	}
	// Not all promotionplans complete — requeue as a safety net in case a
	// PromotionTarget watch event is missed (cache lag, informer backpressure).
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func (r *PromotionRunReconciler) handleTimeout(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) (ctrl.Result, error) {
	patch := client.MergeFrom(promotionrun.DeepCopy())
	r.setRunPhase(promotionrun, kaprov1alpha2.PromotionRunPhaseFailed)
	promotionrun.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf("promotionrun exceeded timeout (%s)", promotionrun.Spec.Timeout)
	r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionFalse, "Timeout", msg)
	r.setStalledCondition(promotionrun, "Timeout", msg)
	if err := r.patchPromotionRunStatus(ctx, promotionrun, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch PromotionRun status (timeout): %w", err)
	}
	r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "Timeout", msg)
	r.notifyPromotionRunEvent(ctx, promotionrun, notification.EventPromotionRunFailed, msg)
	log.FromContext(ctx).Info(msg)
	return ctrl.Result{}, nil
}

func (r *PromotionRunReconciler) handleFailed(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) (ctrl.Result, error) {
	patch := client.MergeFrom(promotionrun.DeepCopy())

	if err := r.loadPromotionTargets(ctx, promotionrun); err != nil {
		return ctrl.Result{}, fmt.Errorf("load promotion targets: %w", err)
	}

	promotionrun.Status.ObservedGeneration = promotionrun.Generation
	promotionrun.Status.Report = r.computeReport(promotionrun)
	r.normalizePromotionRunStatus(promotionrun)
	if err := r.persistPromotionTargets(ctx, promotionrun); err != nil {
		return ctrl.Result{}, fmt.Errorf("persist promotion targets: %w", err)
	}
	hasRollbacks := r.hasActiveRollbackTargets(promotionrun)
	promotionrun.Status.Targets = nil
	r.setPromotionRunReadyCondition(promotionrun, metav1.ConditionFalse, "Failed", "promotionrun failed")

	if hasRollbacks {
		r.setReconcilingCondition(promotionrun, metav1.ConditionTrue, "RollbackInProgress", "promotionrun failed and rollback targets are still progressing")
		r.setStalledCondition(promotionrun, "Failed", "promotionrun failed and rollback is in progress")
	} else {
		r.setReconcilingCondition(promotionrun, metav1.ConditionFalse, "Failed", "promotionrun failed")
		r.setStalledCondition(promotionrun, "Failed", "promotionrun failed")
	}

	if err := r.patchPromotionRunStatus(ctx, promotionrun, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch failed PromotionRun status: %w", err)
	}

	if !hasRollbacks {
		r.clearActivePromotionRun(ctx, promotionrun)
	}
	return ctrl.Result{}, nil
}

// reconcilePromotionPlanStages walks the stage DAG for one promotionplan instance.
//
// For each stage whose dependencies are satisfied it:
//  1. Lists target clusters matching the stage selector.
//  2. Upserts a TargetStatus entry for each (idempotent).
//  3. Observes current target phases → derives stage phase.
//
// Returns (stageProgress, allComplete, anyFailed, error).
// cancelRequest records a stage that needs its pending targets cancelled after
// persistPromotionTargets has run (to avoid the cache overwriting the patch).
type cancelRequest struct {
	promotionplanRef string
	stage            string
}

func (r *PromotionRunReconciler) reconcilePromotionPlanStages(
	ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun,
	promotionplanRefName string,
	promotionplan *kaprov1alpha2.Plan,
) ([]kaprov1alpha2.StageProgress, bool, bool, time.Duration, []cancelRequest, error) {
	log := log.FromContext(ctx)

	// stagePhase maps stage name → "Pending"|"Progressing"|"Complete"|"Failed"
	stagePhase := make(map[string]string, len(promotionplan.Spec.Stages))
	stageProgress := make([]kaprov1alpha2.StageProgress, 0, len(promotionplan.Spec.Stages))

	allComplete := true
	anyFailed := false
	var nextRequeue time.Duration
	var cancels []cancelRequest

	for _, stage := range promotionplan.Spec.Stages {
		// Check stage-level dependencies (with optional soak time and strategy).
		depsComplete := true
		for _, dep := range stage.DependsOn {
			satisfied, wait, err := r.stageDependencySatisfied(ctx, promotionrun, promotionplanRefName, promotionplan, dep)
			if err != nil {
				return nil, false, false, 0, nil, err
			}
			if !satisfied {
				depsComplete = false
				if wait > 0 && (nextRequeue == 0 || wait < nextRequeue) {
					nextRequeue = wait
				}
				break
			}
		}
		if !depsComplete {
			allComplete = false
			stagePhase[stage.Name] = "Pending"
			stageProgress = append(stageProgress, kaprov1alpha2.StageProgress{
				Name: stage.Name, Phase: "Pending",
			})
			continue
		}

		// Plan clusters matching this stage's selector.
		planned, err := r.planTargetsForStage(ctx, promotionplanRefName, promotionplan, stage, promotionrun)
		if err != nil {
			return nil, false, false, 0, nil, fmt.Errorf("list targets for stage %s: %w", stage.Name, err)
		}
		envList := planned.Targets
		if len(envList) == 0 {
			log.Info("stage has no matching clusters — treating as complete",
				"stage", stage.Name, "promotionplan", promotionplan.Name, "promotionplanRef", promotionplanRefName)
			stagePhase[stage.Name] = "Complete"
			stageProgress = append(stageProgress, kaprov1alpha2.StageProgress{
				Name: stage.Name, Phase: "Complete", Total: 0, PlannerResults: apiPlannerResults(planned.Decisions),
			})
			continue
		}

		bindTargets, deferred, strategyDecisions := r.applyStageStrategy(promotionrun, promotionplanRefName, stage, envList)
		plannerResults := apiPlannerResults(append(planned.Decisions, strategyDecisions...))

		resolvedGate, err := resolveStageGate(promotionplan, stage)
		if err != nil {
			return nil, false, false, 0, nil, err
		}

		// Upsert selected target entries; observe phases across the full planned set.
		for _, target := range bindTargets {
			i, err := r.upsertTarget(promotionrun, promotionplanRefName, promotionplan, stage, target, resolvedGate)
			if err != nil {
				return nil, false, false, 0, nil, err
			}
			_ = i
		}

		total, synced, failed := len(envList), 0, 0
		plannedNames := make(map[string]struct{}, len(envList))
		for _, target := range envList {
			plannedNames[target.Name] = struct{}{}
		}
		for _, target := range promotionrun.Status.Targets {
			if target.PlanRef != promotionplanRefName || target.Stage != stage.Name {
				continue
			}
			if _, ok := plannedNames[target.Target]; !ok {
				continue
			}
			switch target.Phase {
			case kaprov1alpha2.TargetPhaseConverged:
				synced++
			case kaprov1alpha2.TargetPhaseSkipped:
				// Skipped targets (onFailure=continue) are terminal — count them
				// as synced so the stage can complete instead of deadlocking.
				synced++
			case kaprov1alpha2.TargetPhaseFailed:
				failed++
			}
		}

		// Derive stage phase from target observations.
		var sp kaprov1alpha2.StageProgress
		sp.Name = stage.Name
		sp.Total = total
		sp.Synced = synced
		sp.Failed = failed
		sp.Deferred = deferred
		sp.PlannerResults = plannerResults

		// Build human-readable message for k9s describe view.
		sp.Message = stageProgressMessage(stage, promotionrun, promotionplanRefName, total, synced, failed, deferred)

		if failed > 0 {
			onFailure := stage.OnFailure
			switch onFailure {
			case kaprov1alpha2.StageFailurePolicySkip:
				log.Info("stage has failed targets with OnFailure=skip, treating as complete",
					"stage", stage.Name, "promotionplanRef", promotionplanRefName, "failed", failed)
				sp.Phase = "Complete"
				stagePhase[stage.Name] = "Complete"
				// Transition Failed targets to Skipped so they are properly terminal
				// and don't pollute the promotionrun report with stale failure counts.
				for idx := range promotionrun.Status.Targets {
					t := &promotionrun.Status.Targets[idx]
					if t.Stage == stage.Name && t.PlanRef == promotionplanRefName && t.Phase == kaprov1alpha2.TargetPhaseFailed {
						t.Phase = kaprov1alpha2.TargetPhaseSkipped
					}
				}
			case kaprov1alpha2.StageFailurePolicyRollback:
				log.Info("stage has failed targets with OnFailure=rollback",
					"stage", stage.Name, "promotionplanRef", promotionplanRefName)
				r.triggerRollbackTargets(ctx, promotionrun, promotionplanRefName, promotionplan, stage.Name)
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
			default: // halt
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
				// Defer cancellation until after persistPromotionTargets to avoid
				// the stale-cache overwriting spec.cancelled.
				cancels = append(cancels, cancelRequest{promotionplanRef: promotionplanRefName, stage: stage.Name})
			}
		} else if synced == total {
			sp.Phase = "Complete"
			stagePhase[stage.Name] = "Complete"
		} else {
			sp.Phase = "Progressing"
			stagePhase[stage.Name] = "Progressing"
			allComplete = false
		}

		prevStagePhase := previousStagePhase(promotionrun, promotionplanRefName, stage.Name)
		if sp.Phase == "Complete" && prevStagePhase != "Complete" {
			r.notifyStageEvent(ctx, promotionrun, promotionplanRefName, stage.Name, notification.EventStageCompleted, "stage completed")
			r.publishStageEvent(ctx, promotionrun, promotionplanRefName, stage.Name, "completed", "Complete",
				fmt.Sprintf("Stage %s/%s completed", promotionplanRefName, stage.Name))
		}
		// kapro.io/promotion.stage.entered fires on the Pending -> Progressing
		// edge. prevStagePhase is "" before the first observation; treat that
		// as Pending so the initial entry emits exactly one event.
		if sp.Phase == "Progressing" && prevStagePhase != "Progressing" && prevStagePhase != "Complete" && prevStagePhase != "Failed" {
			r.publishStageEvent(ctx, promotionrun, promotionplanRefName, stage.Name, "entered", "Progressing",
				fmt.Sprintf("Stage %s/%s entered", promotionplanRefName, stage.Name))
		}
		stageProgress = append(stageProgress, sp)

		if anyFailed {
			break // fail fast within a promotionplan
		}
	}

	return stageProgress, allComplete, anyFailed, nextRequeue, cancels, nil
}

func previousStagePhase(promotionrun *kaprov1alpha2.PromotionRun, promotionplanRef, stageName string) string {
	for _, promotionplanProgress := range promotionrun.Status.PromotionPlanProgress {
		if promotionplanProgress.Name != promotionplanRef {
			continue
		}
		for _, stageProgress := range promotionplanProgress.StageProgress {
			if stageProgress.Name == stageName {
				return stageProgress.Phase
			}
		}
	}
	return ""
}

// previousPromotionPlanPhase returns the phase of the named wave
// (Plan node) as last observed in PromotionRun status. Empty
// when this wave hasn't been recorded yet (first reconcile). Used to
// dedupe kapro.io/promotion.wave.* CloudEvent emission so a single
// transition fires exactly one event.
func previousPromotionPlanPhase(promotionrun *kaprov1alpha2.PromotionRun, promotionplanRef string) string {
	for _, promotionplanProgress := range promotionrun.Status.PromotionPlanProgress {
		if promotionplanProgress.Name == promotionplanRef {
			return promotionplanProgress.Phase
		}
	}
	return ""
}

// publishWaveEvent forwards a kapro.io/promotion.wave.* emission to the
// optional StagePublisher (operator-level CloudEvents sink). Nil-safe.
func (r *PromotionRunReconciler) publishWaveEvent(ctx context.Context,
	run *kaprov1alpha2.PromotionRun, wave, kind, phase, reason string) {
	if r.StagePublisher == nil {
		return
	}
	r.StagePublisher.PublishWaveEvent(ctx, run, wave, kind, phase, reason)
}

// publishStageEvent forwards a kapro.io/promotion.stage.* emission.
// Nil-safe.
func (r *PromotionRunReconciler) publishStageEvent(ctx context.Context,
	run *kaprov1alpha2.PromotionRun, wave, stage, kind, phase, reason string) {
	if r.StagePublisher == nil {
		return
	}
	r.StagePublisher.PublishStageEvent(ctx, run, wave, stage, kind, phase, reason)
}

// stageProgressMessage builds a human-readable status line for k9s describe view.
// Examples: "3/5 converged", "blocked: waiting for approval on de-prod", "1/8 failed"
func stageProgressMessage(stage kaprov1alpha2.Stage, promotionrun *kaprov1alpha2.PromotionRun, promotionplanRef string, total, synced, failed, deferred int) string {
	if total == 0 {
		return "no matching clusters"
	}
	if synced == total {
		return fmt.Sprintf("%d/%d converged", synced, total)
	}

	// Find the most interesting phase among non-terminal targets.
	waitingApproval := 0
	applying := 0
	soaking := 0
	metricsCheck := 0
	for i := range promotionrun.Status.Targets {
		t := &promotionrun.Status.Targets[i]
		if t.Stage != stage.Name || t.PlanRef != promotionplanRef {
			continue
		}
		switch t.Phase {
		case kaprov1alpha2.TargetPhaseWaitingApproval:
			waitingApproval++
		case kaprov1alpha2.TargetPhaseApplying:
			applying++
		case kaprov1alpha2.TargetPhaseSoaking:
			soaking++
		case kaprov1alpha2.TargetPhaseMetricsCheck:
			metricsCheck++
		}
	}

	parts := fmt.Sprintf("%d/%d converged", synced, total)
	if failed > 0 {
		parts += fmt.Sprintf(", %d failed", failed)
	}
	if waitingApproval > 0 {
		parts += fmt.Sprintf(", %d awaiting approval", waitingApproval)
	}
	if applying > 0 {
		parts += fmt.Sprintf(", %d applying", applying)
	}
	if soaking > 0 {
		parts += fmt.Sprintf(", %d soaking", soaking)
	}
	if metricsCheck > 0 {
		parts += fmt.Sprintf(", %d checking metrics", metricsCheck)
	}
	if deferred > 0 {
		parts += fmt.Sprintf(", %d deferred", deferred)
	}
	return parts
}

func (r *PromotionRunReconciler) stageDependencySatisfied(
	ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun,
	promotionplanRefName string,
	promotionplan *kaprov1alpha2.Plan,
	dep kaprov1alpha2.StageDependency,
) (bool, time.Duration, error) {
	depStage, ok := promotionplanStageByName(promotionplan, dep.Stage)
	if !ok {
		return false, 0, fmt.Errorf("stage dependency %q not found in promotionplan %s", dep.Stage, promotionplan.Name)
	}

	planned, err := r.planTargetsForStage(ctx, promotionplanRefName, promotionplan, depStage, promotionrun)
	if err != nil {
		return false, 0, fmt.Errorf("list dependency targets for stage %s: %w", dep.Stage, err)
	}
	targets := planned.Targets
	if len(targets) == 0 {
		return true, 0, nil
	}

	expected := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		expected[target.Name] = struct{}{}
	}

	strategy := dep.Strategy
	if strategy == "" {
		strategy = kaprov1alpha2.StageDependencyAll
	}

	soak := time.Duration(0)
	if dep.RequiredSoakTime != nil {
		soak = dep.RequiredSoakTime.Duration
	}

	now := time.Now().UTC()
	successful := 0
	var shortestWait time.Duration

	for idx := range promotionrun.Status.Targets {
		target := &promotionrun.Status.Targets[idx]
		if target.PlanRef != promotionplanRefName || target.Stage != dep.Stage {
			continue
		}
		if _, ok := expected[target.Target]; !ok {
			continue
		}
		if !dependencyTargetSucceeded(target.Phase) {
			continue
		}

		successful++
		if soak == 0 {
			if strategy == kaprov1alpha2.StageDependencyAny {
				return true, 0, nil
			}
			continue
		}

		remaining := dependencySoakRemaining(target.FinishedAt, now, soak)
		if remaining <= 0 {
			if strategy == kaprov1alpha2.StageDependencyAny {
				return true, 0, nil
			}
			continue
		}
		if shortestWait == 0 || remaining < shortestWait {
			shortestWait = remaining
		}
	}

	switch strategy {
	case kaprov1alpha2.StageDependencyAny:
		return false, shortestWait, nil
	case kaprov1alpha2.StageDependencyAll:
		if successful < len(expected) {
			return false, 0, nil
		}
		return shortestWait == 0, shortestWait, nil
	default:
		return false, 0, fmt.Errorf("stage dependency %q has unsupported strategy %q", dep.Stage, dep.Strategy)
	}
}

func promotionplanStageByName(promotionplan *kaprov1alpha2.Plan, name string) (kaprov1alpha2.Stage, bool) {
	for _, stage := range promotionplan.Spec.Stages {
		if stage.Name == name {
			return stage, true
		}
	}
	return kaprov1alpha2.Stage{}, false
}

func dependencyTargetSucceeded(phase kaprov1alpha2.TargetPhase) bool {
	return phase == kaprov1alpha2.TargetPhaseConverged || phase == kaprov1alpha2.TargetPhaseSkipped
}

func dependencySoakRemaining(finishedAt string, now time.Time, soak time.Duration) time.Duration {
	if finishedAt == "" {
		return soak
	}
	finished, err := time.Parse(time.RFC3339, finishedAt)
	if err != nil {
		return soak
	}
	if elapsed := now.Sub(finished); elapsed < soak {
		return soak - elapsed
	}
	return 0
}

// listRawTargetsForStage returns all FleetClusters that match the stage selector,
// filtered to spec.scope.targets when a scope is set on the PromotionRun.
func (r *PromotionRunReconciler) listRawTargetsForStage(ctx context.Context, stage kaprov1alpha2.Stage, promotionrun *kaprov1alpha2.PromotionRun) ([]kaprov1alpha2.Cluster, error) {
	var mcList kaprov1alpha2.ClusterList
	sel, err := metav1.LabelSelectorAsSelector(&stage.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid stage selector: %w", err)
	}
	listOpts := []client.ListOption{
		client.MatchingLabelsSelector{Selector: sel},
	}
	if err := r.List(ctx, &mcList, listOpts...); err != nil {
		return nil, err
	}
	clusters := mcList.Items

	// Filter out suspended clusters — spec.suspend means "do not deploy to this cluster".
	filtered := clusters[:0]
	for _, mc := range clusters {
		if mc.Spec.Suspend {
			log.FromContext(ctx).Info("skipping suspended cluster", "cluster", mc.Name, "stage", stage.Name)
			continue
		}
		filtered = append(filtered, mc)
	}
	clusters = filtered

	// Apply scope filter when an explicit cluster allowlist is provided.
	if promotionrun.Spec.Scope != nil && len(promotionrun.Spec.Scope.Targets) > 0 {
		allowed := make(map[string]struct{}, len(promotionrun.Spec.Scope.Targets))
		for _, t := range promotionrun.Spec.Scope.Targets {
			allowed[t] = struct{}{}
		}
		scopeFiltered := clusters[:0]
		for _, mc := range clusters {
			if _, ok := allowed[mc.Name]; ok {
				scopeFiltered = append(scopeFiltered, mc)
			}
		}
		if len(scopeFiltered) == 0 && len(clusters) > 0 {
			log.FromContext(ctx).Info("scope filter eliminated all clusters for stage — treating as no-op",
				"stage", stage.Name, "scopeTargets", promotionrun.Spec.Scope.Targets)
		}
		clusters = scopeFiltered
	}

	return clusters, nil
}

// listTargetsForStage returns the planned FleetClusters for a stage.
func (r *PromotionRunReconciler) listTargetsForStage(ctx context.Context, promotionplanRefName string, promotionplan *kaprov1alpha2.Plan, stage kaprov1alpha2.Stage, promotionrun *kaprov1alpha2.PromotionRun) ([]kaprov1alpha2.Cluster, error) {
	planned, err := r.planTargetsForStage(ctx, promotionplanRefName, promotionplan, stage, promotionrun)
	if err != nil {
		return nil, err
	}
	return planned.Targets, nil
}

// planTargetsForStage runs the scheduler-style planner for a stage and returns
// both eligible targets and recorded skip decisions.
func (r *PromotionRunReconciler) planTargetsForStage(ctx context.Context, promotionplanRefName string, promotionplan *kaprov1alpha2.Plan, stage kaprov1alpha2.Stage, promotionrun *kaprov1alpha2.PromotionRun) (planner.Result, error) {
	clusters, err := r.listRawTargetsForStage(ctx, stage, promotionrun)
	if err != nil {
		return planner.Result{}, err
	}
	framework := r.Planner
	if framework == nil {
		framework = planner.NewDefaultFramework()
	}
	return framework.PlanWithResult(ctx, planner.Request{
		PromotionRun:         promotionrun,
		PromotionPlanRefName: promotionplanRefName,
		Plan:        promotionplan,
		Stage:                stage,
	}, clusters)
}

func (r *PromotionRunReconciler) applyStageStrategy(
	promotionrun *kaprov1alpha2.PromotionRun,
	promotionplanRefName string,
	stage kaprov1alpha2.Stage,
	targets []kaprov1alpha2.Cluster,
) ([]kaprov1alpha2.Cluster, int, []planner.Decision) {
	if stage.Strategy == nil || stage.Strategy.MaxParallel <= 0 {
		return targets, 0, nil
	}

	planned := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		planned[target.Name] = struct{}{}
	}

	active := 0
	bound := make(map[string]struct{}, len(targets))
	for _, target := range promotionrun.Status.Targets {
		if target.PlanRef != promotionplanRefName || target.Stage != stage.Name {
			continue
		}
		if _, ok := planned[target.Target]; !ok {
			continue
		}
		bound[target.Target] = struct{}{}
		if !planningTargetTerminal(target.Phase) {
			active++
		}
	}

	capacity := int(stage.Strategy.MaxParallel) - active
	if capacity < 0 {
		capacity = 0
	}

	bindTargets := make([]kaprov1alpha2.Cluster, 0, len(targets))
	decisions := make([]planner.Decision, 0)
	deferred := 0
	for _, target := range targets {
		if _, ok := bound[target.Name]; ok {
			continue
		}
		if capacity > 0 {
			bindTargets = append(bindTargets, target)
			capacity--
			continue
		}
		deferred++
		decisions = append(decisions, planner.Decision{
			Target:  target.Name,
			Plugin:  "stage-strategy",
			Phase:   "Bind",
			Reason:  "MaxParallel",
			Message: fmt.Sprintf("deferred by stage strategy maxParallel=%d", stage.Strategy.MaxParallel),
		})
	}

	return bindTargets, deferred, decisions
}

func planningTargetTerminal(phase kaprov1alpha2.TargetPhase) bool {
	return phase == kaprov1alpha2.TargetPhaseConverged ||
		phase == kaprov1alpha2.TargetPhaseFailed ||
		phase == kaprov1alpha2.TargetPhaseSkipped
}

func apiPlannerResults(decisions []planner.Decision) []kaprov1alpha2.PlannerResult {
	if len(decisions) == 0 {
		return nil
	}
	limit := len(decisions)
	if limit > maxPlannerResultsPerStage {
		limit = maxPlannerResultsPerStage
	}
	results := make([]kaprov1alpha2.PlannerResult, 0, limit)
	for i := 0; i < limit; i++ {
		decision := decisions[i]
		results = append(results, kaprov1alpha2.PlannerResult{
			Target:  decision.Target,
			Plugin:  decision.Plugin,
			Phase:   decision.Phase,
			Reason:  decision.Reason,
			Message: decision.Message,
		})
	}
	return results
}

// upsertTarget finds an existing TargetStatus entry for
// (promotionplanRefName, stage.Name, mc.Name) or appends a new one.
// Returns the slice index of the entry (stable within a single reconcile).

func resolveStageGate(promotionplan *kaprov1alpha2.Plan, stage kaprov1alpha2.Stage) (*kaprov1alpha2.GatePolicySpec, error) {
	if stage.Gate == nil {
		return nil, nil
	}
	gatePolicy := stage.Gate.DeepCopy()
	if len(gatePolicy.Gate.Metrics) == 0 {
		return gatePolicy, nil
	}
	presets := map[string]kaprov1alpha2.MetricGate{}
	if promotionplan != nil {
		presets = promotionplan.Spec.MetricPresets
	}
	for i, metric := range gatePolicy.Gate.Metrics {
		if metric.Preset == "" {
			continue
		}
		preset, ok := presets[metric.Preset]
		if !ok {
			return nil, fmt.Errorf("stage %q metric[%d] references unknown metric preset %q", stage.Name, i, metric.Preset)
		}
		gatePolicy.Gate.Metrics[i] = mergeMetricPreset(preset, metric)
	}
	return gatePolicy, nil
}

func mergeMetricPreset(preset, override kaprov1alpha2.MetricGate) kaprov1alpha2.MetricGate {
	out := preset
	out.Preset = override.Preset
	if override.Provider != "" {
		out.Provider = override.Provider
	}
	if override.Query != "" {
		out.Query = override.Query
	}
	if override.Window != "" {
		out.Window = override.Window
	}
	if override.Interval != "" {
		out.Interval = override.Interval
	}
	if override.Endpoint != "" {
		out.Endpoint = override.Endpoint
	}
	if override.Threshold != nil {
		out.Threshold = override.Threshold
	}
	if len(override.Config) > 0 {
		out.Config = override.Config
	}
	return out
}

// triggerRollbackTargets appends rollback TargetStatus entries for every
// converged target in the failed stage and all earlier stages in the same
// promotionplan instance. In-memory only; caller patches.

// cancelPendingStageTargets signals non-terminal targets in the stage to stop.
// This implements failurePolicy: halt — sibling targets stop advancing.
//
// Ownership contract: the parent writes spec.cancelled (parent owns spec),
// the child PromotionTargetReconciler observes it and transitions to Failed
// (child owns status). This avoids cross-controller status writes.

// clearActivePromotionRun clears mc.status.activePromotionRun for all FleetClusters
// targeted by this PromotionRun, found via promotionrun.Status.Targets.

func promotionTargetObjectName(target kaprov1alpha2.TargetExecutionState) string {
	name := syncName(target.PromotionRunRef, target.PlanRef, target.Stage, target.Target)
	if target.Rollback {
		return name + "-rollback"
	}
	return name
}

// PromotionTargetObjectNameForTest exposes the deterministic child-object naming
// contract to external tests without widening production behavior.
func PromotionTargetObjectNameForTest(target kaprov1alpha2.TargetExecutionState) string {
	return promotionTargetObjectName(target)
}

func targetStatusFromPromotionTarget(rt *kaprov1alpha2.Target) kaprov1alpha2.TargetExecutionState {
	target := rt.Status.TargetExecutionState
	target.PromotionRunRef = rt.Spec.PromotionRunRef
	target.Target = rt.Spec.Target
	target.PlanRef = rt.Spec.PlanRef
	target.Plan = rt.Spec.Plan
	target.Stage = rt.Spec.Stage
	target.Version = rt.Spec.Version
	target.Gate = rt.Spec.Gate
	target.AppKey = rt.Spec.AppKey
	target.DesiredVersions = copyStringMap(rt.Spec.DesiredVersions)
	target.Rollback = rt.Spec.Rollback
	return target
}

// persistPromotionTargets ensures a PromotionTarget CRD exists for each in-memory
// target entry. The parent creates new children and updates their specs/labels/
// ownerRefs, but NEVER writes child status — that's owned by PromotionTargetReconciler.

// handleDeletion clears FleetCluster activePromotionRun references and removes the finalizer.
// Targets are inline status — nothing to delete externally.
func (r *PromotionRunReconciler) handleDeletion(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("handling PromotionRun deletion", "name", promotionrun.Name)

	// Ensure targets are loaded so clearActivePromotionRun can find all clusters to
	// clear. If this fails, retry deletion rather than removing the finalizer
	// with stale activePromotionRun claims still pointing at this PromotionRun.
	if len(promotionrun.Status.Targets) == 0 {
		if err := r.loadPromotionTargets(ctx, promotionrun); err != nil {
			return ctrl.Result{}, fmt.Errorf("handleDeletion: load promotion targets for cleanup: %w", err)
		}
	}
	r.clearActivePromotionRun(ctx, promotionrun)

	patch := client.MergeFrom(promotionrun.DeepCopy())
	controllerutil.RemoveFinalizer(promotionrun, promotionrunFinalizer)
	if err := r.Patch(ctx, promotionrun, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}

	log.Info("PromotionRun cleanup complete", "name", promotionrun.Name)
	return ctrl.Result{}, nil
}

func (r *PromotionRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	// Index Approvals by promotionrun label — used to map Approval changes back to
	// the owning PromotionRun.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha2.Approval{}, IndexKeyPromotionRun,
		labelExtractor(IndexKeyPromotionRun),
	); err != nil {
		return fmt.Errorf("index Approval by %s: %w", IndexKeyPromotionRun, err)
	}

	// Index PromotionTargets by owning PromotionRun and target cluster so FleetCluster
	// and PromotionTarget watches can route directly to affected PromotionRuns.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha2.Target{}, IndexKeyActiveCluster,
		ActiveClusterExtractor,
	); err != nil {
		return fmt.Errorf("index PromotionTarget by %s: %w", IndexKeyActiveCluster, err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha2.Target{}, IndexKeyPromotionTargetPromotionRun,
		PromotionTargetPromotionRunExtractor,
	); err != nil {
		return fmt.Errorf("index PromotionTarget by %s: %w", IndexKeyPromotionTargetPromotionRun, err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha2.PromotionRun{}, IndexKeyPromotionRunProgressing,
		PromotionRunProgressingExtractor,
	); err != nil {
		return fmt.Errorf("index PromotionRun by %s: %w", IndexKeyPromotionRunProgressing, err)
	}

	forPredicates := []predicate.Predicate{predicate.GenerationChangedPredicate{}}
	if r.ShardPredicate != nil {
		forPredicates = append(forPredicates, r.ShardPredicate)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		For(&kaprov1alpha2.PromotionRun{},
			builder.WithPredicates(forPredicates...),
		).
		// Watch FleetClusters — if a new cluster is registered whose labels match
		// an in-progress stage, wake up the PromotionRun so a new target entry is created.
		Watches(
			&kaprov1alpha2.Cluster{},
			handler.EnqueueRequestsFromMapFunc(r.promotionrunsForFleetCluster),
			builder.WithPredicates(promotionrunFleetClusterPredicates()),
		).
		// Watch Approvals — when an Approval CR is created for a WaitingApproval target,
		// wake up the PromotionRun so the target can advance to Applying.
		Watches(
			&kaprov1alpha2.Approval{},
			handler.EnqueueRequestsFromMapFunc(approvalForPromotionRun),
		).
		Watches(
			&kaprov1alpha2.Target{},
			handler.EnqueueRequestsFromMapFunc(promotionrunForTarget),
		).
		Complete(r)
}

func promotionrunFleetClusterPredicates() predicate.Predicate {
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
			if !labels.Equals(labels.Set(oldMC.GetLabels()), labels.Set(newMC.GetLabels())) {
				return true
			}
			return false
		},
	}
}

func (r *PromotionRunReconciler) promotionrunsForFleetCluster(ctx context.Context, obj client.Object) []ctrl.Request {
	mc, ok := obj.(*kaprov1alpha2.Cluster)
	if !ok {
		return nil
	}
	// Use the active-cluster field index to find only promotion targets that
	// reference this specific cluster. This avoids scanning the entire PromotionRun
	// fleet on every FleetCluster status update.
	var targetList kaprov1alpha2.TargetList
	if err := r.List(ctx, &targetList,
		client.MatchingFields{IndexKeyActiveCluster: mc.Name},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list promotion targets for fleet cluster", "cluster", mc.Name)
		return nil
	}
	if len(targetList.Items) == 0 {
		return r.progressingPromotionRunsForNewCluster(ctx, mc)
	}
	seen := make(map[client.ObjectKey]struct{}, len(targetList.Items))
	reqs := make([]ctrl.Request, 0, len(targetList.Items))
	for i := range targetList.Items {
		rt := &targetList.Items[i]
		key := client.ObjectKey{Name: rt.Spec.PromotionRunRef, Namespace: rt.Namespace}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		var rel kaprov1alpha2.PromotionRun
		if err := r.Get(ctx, key, &rel); err != nil {
			continue
		}
		if rel.Status.Phase == kaprov1alpha2.PromotionRunPhaseComplete || rel.Status.Phase == kaprov1alpha2.PromotionRunPhaseFailed {
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: key})
	}
	return reqs
}

// PromotionRunsForFleetClusterForTest exposes the watch-mapper logic to external
// tests without widening the production watch contract.
func (r *PromotionRunReconciler) PromotionRunsForFleetClusterForTest(ctx context.Context, mc *kaprov1alpha2.Cluster) []ctrl.Request {
	return r.promotionrunsForFleetCluster(ctx, mc)
}

// ProgressingPromotionRunsForNewClusterForTest exposes the new-cluster fallback path
// to external tests.
func (r *PromotionRunReconciler) ProgressingPromotionRunsForNewClusterForTest(ctx context.Context, mc *kaprov1alpha2.Cluster) []ctrl.Request {
	return r.progressingPromotionRunsForNewCluster(ctx, mc)
}

// progressingPromotionRunsForNewCluster handles the case where a newly registered
// cluster is not yet referenced by any PromotionRun.status.targets entry. The
// active-cluster index cannot find these promotionruns, so we fall back to checking
// only non-terminal promotionruns and enqueue those whose Plan selectors could
// target the cluster.
func (r *PromotionRunReconciler) progressingPromotionRunsForNewCluster(ctx context.Context, mc *kaprov1alpha2.Cluster) []ctrl.Request {
	var promotionrunList kaprov1alpha2.PromotionRunList
	if err := r.List(ctx, &promotionrunList, client.MatchingFields{IndexKeyPromotionRunProgressing: "true"}); err != nil {
		// Some tests and ad-hoc fake clients do not register field indexes. Fall back
		// to a full list there; production SetupWithManager always installs the index.
		if err := r.List(ctx, &promotionrunList); err != nil {
			log.FromContext(ctx).Error(err, "failed to list promotionruns for new cluster fallback", "cluster", mc.Name)
			return nil
		}
	}

	promotionplanCache := make(map[string]*kaprov1alpha2.Plan)
	reqs := make([]ctrl.Request, 0)

	for i := range promotionrunList.Items {
		rel := &promotionrunList.Items[i]
		if rel.Status.Phase == kaprov1alpha2.PromotionRunPhaseComplete || rel.Status.Phase == kaprov1alpha2.PromotionRunPhaseFailed {
			continue
		}
		if !promotionrunScopeAllowsCluster(rel, mc.Name) {
			continue
		}
		if r.promotionrunCouldTargetCluster(ctx, rel, mc, promotionplanCache) {
			reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rel)})
		}
	}

	return reqs
}

func promotionrunScopeAllowsCluster(promotionrun *kaprov1alpha2.PromotionRun, clusterName string) bool {
	if promotionrun.Spec.Scope == nil || len(promotionrun.Spec.Scope.Targets) == 0 {
		return true
	}
	for _, name := range promotionrun.Spec.Scope.Targets {
		if name == clusterName {
			return true
		}
	}
	return false
}

func (r *PromotionRunReconciler) promotionrunCouldTargetCluster(
	ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun,
	mc *kaprov1alpha2.Cluster,
	promotionplanCache map[string]*kaprov1alpha2.Plan,
) bool {
	for _, ref := range promotionrun.Spec.PromotionPlans {
		promotionplan, ok := promotionplanCache[ref.Plan]
		if !ok {
			var fetched kaprov1alpha2.Plan
			if err := r.Get(ctx, client.ObjectKey{Name: ref.Plan}, &fetched); err != nil {
				continue
			}
			promotionplanCache[ref.Plan] = &fetched
			promotionplan = &fetched
		}
		for _, stage := range promotionplan.Spec.Stages {
			selector, err := metav1.LabelSelectorAsSelector(&stage.Selector)
			if err != nil {
				continue
			}
			if selector.Matches(labels.Set(mc.Labels)) {
				return true
			}
		}
	}
	return false
}

func promotionrunForTarget(_ context.Context, obj client.Object) []ctrl.Request {
	rt, ok := obj.(*kaprov1alpha2.Target)
	if !ok {
		return nil
	}
	if rt.Spec.PromotionRunRef == "" {
		return nil
	}
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{
			Name:      rt.Spec.PromotionRunRef,
			Namespace: rt.Namespace,
		},
	}}
}

// syncKey builds a unique map key for one target rollout entry:
// <promotionplanRefName>/<stage>/<target>.
func syncKey(promotionplanRefName, stage, target string) string {
	return promotionplanRefName + "/" + stage + "/" + target
}

// syncName builds the deterministic name for one target rollout entry.
// Format: <promotionrun-prefix>-<hashed logical key>. The hash makes the name
// collision-safe even when individual units contain hyphens.
func syncName(promotionrun, promotionplanRef, stage, target string) string {
	key := fmt.Sprintf("%s/%s", promotionrun, syncKey(promotionplanRef, stage, target))
	h := fnv.New32a()
	_, _ = fmt.Fprint(h, key)
	prefix := promotionrun
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}
	return fmt.Sprintf("%s-%08x", prefix, h.Sum32())
}

// promotionrunAppKey returns the key used in FleetCluster.status.currentVersions.
func promotionrunAppKey(promotionrun *kaprov1alpha2.PromotionRun) string {
	return "default"
}

func promotionrunDesiredVersionsFromSpec(promotionrun *kaprov1alpha2.PromotionRun) map[string]string {
	desired := make(map[string]string, len(promotionrun.Spec.Versions)+1)
	if promotionrun.Spec.Version != "" {
		desired[promotionrunAppKey(promotionrun)] = promotionrun.Spec.Version
	}
	for unit, version := range promotionrun.Spec.Versions {
		if unit == "" || version == "" {
			continue
		}
		desired[unit] = version
	}
	if len(desired) == 0 {
		return nil
	}
	return desired
}

func promotionrunDesiredVersions(promotionrun *kaprov1alpha2.PromotionRun) map[string]string {
	if len(promotionrun.Spec.Versions) > 0 {
		return promotionrunDesiredVersionsFromSpec(promotionrun)
	}
	if promotionrun.Status.ResolvedVersion == "" {
		return nil
	}
	return map[string]string{"default": promotionrun.Status.ResolvedVersion}
}

func promotionrunPrimaryVersion(promotionrun *kaprov1alpha2.PromotionRun, desired map[string]string) string {
	if version := desired[promotionrunAppKey(promotionrun)]; version != "" {
		return version
	}
	keys := make([]string, 0, len(desired))
	for unit := range desired {
		keys = append(keys, unit)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return desired[keys[0]]
}

func primaryDesiredVersion(desired map[string]string, fallbackVersion, fallbackAppKey string) (string, string) {
	if len(desired) == 0 {
		return fallbackVersion, fallbackAppKey
	}
	keys := make([]string, 0, len(desired))
	for appKey := range desired {
		keys = append(keys, appKey)
	}
	sort.Strings(keys)
	appKey := keys[0]
	return desired[appKey], appKey
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (r *PromotionRunReconciler) setPromotionRunReadyCondition(promotionrun *kaprov1alpha2.PromotionRun, status metav1.ConditionStatus, reason, message string) {
	if len(message) > maxPromotionRunReadyMessageSize {
		message = message[:maxPromotionRunReadyMessageSize]
	}
	apimeta.SetStatusCondition(&promotionrun.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		ObservedGeneration: promotionrun.Generation,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func (r *PromotionRunReconciler) setReconcilingCondition(promotionrun *kaprov1alpha2.PromotionRun, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&promotionrun.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeReconciling,
		Status:             status,
		Reason:             reason,
		ObservedGeneration: promotionrun.Generation,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func (r *PromotionRunReconciler) setStalledCondition(promotionrun *kaprov1alpha2.PromotionRun, reason, message string) {
	apimeta.SetStatusCondition(&promotionrun.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeStalled,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		ObservedGeneration: promotionrun.Generation,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func (r *PromotionRunReconciler) clearStalledCondition(promotionrun *kaprov1alpha2.PromotionRun) {
	apimeta.RemoveStatusCondition(&promotionrun.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
}

func promotionrunProgressSummary(promotionrun *kaprov1alpha2.PromotionRun) string {
	activePromotionPlans := 0
	for _, p := range promotionrun.Status.PromotionPlanProgress {
		if p.Phase == "Progressing" || p.Phase == "Pending" {
			activePromotionPlans++
		}
	}

	activeTargets := 0
	for _, target := range promotionrun.Status.Targets {
		if target.Rollback {
			continue
		}
		if target.Phase != kaprov1alpha2.TargetPhaseConverged && target.Phase != kaprov1alpha2.TargetPhaseFailed {
			activeTargets++
		}
	}

	return fmt.Sprintf("promotionrun progressing: %d active promotionplans, %d active targets", activePromotionPlans, activeTargets)
}

// normalizePromotionRunStatus deduplicates PromotionRun.status.targets and bounds per-target
// gate history. It never drops target execution rows, because those rows are the
// source of truth for in-flight rollout state.
func (r *PromotionRunReconciler) normalizePromotionRunStatus(promotionrun *kaprov1alpha2.PromotionRun) {
	if len(promotionrun.Status.Targets) == 0 {
		return
	}

	// Keep the latest current-state row for each logical target, plus at most one
	// rollback row. This prevents PromotionRun.status.targets from becoming an append-only log.
	seen := make(map[string]struct{}, len(promotionrun.Status.Targets))
	normalized := make([]kaprov1alpha2.TargetExecutionState, 0, len(promotionrun.Status.Targets))
	for i := len(promotionrun.Status.Targets) - 1; i >= 0; i-- {
		target := promotionrun.Status.Targets[i]
		r.normalizeTargetEntry(&target)
		key := syncKey(target.PlanRef, target.Stage, target.Target)
		if target.Rollback {
			key += "/rollback"
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, target)
	}

	for i, j := 0, len(normalized)-1; i < j; i, j = i+1, j-1 {
		normalized[i], normalized[j] = normalized[j], normalized[i]
	}

	promotionrun.Status.Targets = normalized
}

func (r *PromotionRunReconciler) normalizeTargetEntry(target *kaprov1alpha2.TargetExecutionState) {
	if len(target.Gates) > maxGateRunsPerTarget {
		target.Gates = target.Gates[len(target.Gates)-maxGateRunsPerTarget:]
	}
	for i := range target.Gates {
		if len(target.Gates[i].Results) > maxGateResultsPerGateRun {
			target.Gates[i].Results = target.Gates[i].Results[len(target.Gates[i].Results)-maxGateResultsPerGateRun:]
		}
	}
}

// appendAuditEntry records the delivery provenance of a completed PromotionRun in
// PromotionRun.status.auditTrail. It is idempotent — an entry for the same PromotionRun
// version is only appended once. AuditTrail is capped at 50 entries (oldest trimmed).
// This method modifies promotionrun.Status.AuditTrail in-place; the caller must include
// promotionrun in a status patch for the change to persist.
func (r *PromotionRunReconciler) appendAuditEntry(_ context.Context, promotionrun *kaprov1alpha2.PromotionRun) {
	// Idempotency: already have an entry for this promotionrun.
	for _, e := range promotionrun.Status.AuditTrail {
		if e.PromotionRun == promotionrun.Name && e.Artifact == promotionrun.Spec.Version {
			return
		}
	}

	var scope []string
	if promotionrun.Spec.Scope != nil {
		scope = promotionrun.Spec.Scope.Targets
	}

	entry := kaprov1alpha2.AuditEntry{
		Artifact:     promotionrun.Spec.Version,
		PromotionRun: promotionrun.Name,
		Scope:        scope,
		CompletedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	promotionrun.Status.AuditTrail = append(promotionrun.Status.AuditTrail, entry)

	const maxAuditTrail = 50
	if len(promotionrun.Status.AuditTrail) > maxAuditTrail {
		promotionrun.Status.AuditTrail = promotionrun.Status.AuditTrail[len(promotionrun.Status.AuditTrail)-maxAuditTrail:]
	}
}

// computeReport builds the inline PromotionRunReportSummary from PromotionRun.status.targets.
// It is a bounded, counter-only summary; per-target detail lives in status.targets.
func (r *PromotionRunReconciler) computeReport(promotionrun *kaprov1alpha2.PromotionRun) kaprov1alpha2.PromotionRunReportSummary {
	now := time.Now().UTC()

	st := kaprov1alpha2.PromotionRunReportSummary{
		Phase:           promotionrun.Status.Phase,
		Artifact:        promotionrun.Spec.Version,
		ResolvedVersion: promotionrun.Status.ResolvedVersion,
		StartedAt:       promotionrun.Status.StartedAt,
		CompletedAt:     promotionrun.Status.CompletedAt,
	}
	st.TotalArtifacts = 1
	st.DeltaArtifacts = 1

	if st.StartedAt != "" {
		if started, err := time.Parse(time.RFC3339, st.StartedAt); err == nil {
			end := now
			if st.CompletedAt != "" {
				if completed, err := time.Parse(time.RFC3339, st.CompletedAt); err == nil {
					end = completed
				}
			}
			st.Duration = end.Sub(started).Round(time.Second).String()
		}
	}

	// Count targets from inline status; list pending approvals by deterministic name.
	// Key by (promotionplanRef, stage, cluster) to avoid undercounting when the same cluster
	// is targeted by multiple promotionplans or stages.
	targetPhases := make(map[string]kaprov1alpha2.TargetPhase, len(promotionrun.Status.Targets))
	var rolledBack int
	var pendingApprovals []string
	for _, target := range promotionrun.Status.Targets {
		if target.Rollback {
			rolledBack++
			continue
		}
		key := target.PlanRef + "\x00" + target.Stage + "\x00" + target.Target
		targetPhases[key] = target.Phase
		if target.Phase == kaprov1alpha2.TargetPhaseWaitingApproval {
			pendingApprovals = append(pendingApprovals, internalgate.ApprovalName(promotionrun.Name, syncName(promotionrun.Name, target.PlanRef, target.Stage, target.Target)))
		}
	}

	var totalTargets, synced, failed, pending int
	for _, phase := range targetPhases {
		totalTargets++
		switch phase {
		case kaprov1alpha2.TargetPhaseConverged, kaprov1alpha2.TargetPhaseSkipped:
			synced++
		case kaprov1alpha2.TargetPhaseFailed:
			failed++
		default:
			pending++
		}
	}
	st.TotalTargets = totalTargets
	st.SyncedTargets = synced
	st.FailedTargets = failed
	st.PendingTargets = pending
	st.RolledBackTargets = rolledBack
	st.PendingApprovals = pendingApprovals

	return st
}
