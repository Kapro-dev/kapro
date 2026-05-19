package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	promotionIntentRequeue = 15 * time.Second
	promotionOwnerLabel    = "kapro.io/promotion"
	promotionSpecHashLabel = "kapro.io/promotion-spec-hash"
	supersededReason       = "SupersededByNewPromotionAttempt"
)

// PromotionReconciler materializes Promotion intent into PromotionRun
// attempts. New attempts are stamped whenever the Promotion spec hash
// changes; previous non-terminal runs are marked Superseded.
type PromotionReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
	// Lifecycle dispatches Promotion.spec.lifecycle.handlers on coarse
	// phase transitions. Nil-safe: when unset, transitions emit the
	// built-in phase event only and no user handlers fire.
	Lifecycle PhaseTransitionDispatcher
}

// PhaseTransitionDispatcher is the minimal interface the PromotionReconciler
// needs to fan out lifecycle handlers. Defined here (rather than imported)
// so the dispatcher implementation and the controller can be tested in
// isolation without import cycles.
type PhaseTransitionDispatcher interface {
	OnPhaseTransition(ctx context.Context, promotion *kaprov1alpha1.Promotion,
		prev, next kaprov1alpha1.PromotionPhase)
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=kaproes,verbs=get;list;watch

func (r *PromotionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var promotion kaprov1alpha1.Promotion
	if err := r.Get(ctx, req.NamespacedName, &promotion); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !promotion.DeletionTimestamp.IsZero() {
		// Docker-lifecycle analogue: removing. Owned PromotionRuns are
		// garbage-collected by ownerReference; we just publish the phase so
		// observers see the transition.
		return r.patchStatus(ctx, &promotion, kaprov1alpha1.PromotionPhaseTerminating,
			"", "Terminating", "deletionTimestamp set; child PromotionRuns will be garbage-collected")
	}

	// Resolve parent Kapro (cluster-scoped, looked up by name).
	var parent kaprov1alpha1.Kapro
	if err := r.Get(ctx, client.ObjectKey{Name: promotion.Spec.KaproRef}, &parent); err != nil {
		if apierrors.IsNotFound(err) {
			return r.patchUnresolved(ctx, &promotion, "KaproNotFound",
				fmt.Sprintf("referenced Kapro %q does not exist", promotion.Spec.KaproRef))
		}
		return ctrl.Result{}, fmt.Errorf("get parent Kapro %q: %w", promotion.Spec.KaproRef, err)
	}

	specHash := promotionSpecHash(&promotion.Spec)

	if promotion.Spec.Suspended || parent.Spec.Suspended {
		if err := r.suspendOwnedRuns(ctx, &promotion); err != nil {
			return ctrl.Result{}, err
		}
		return r.patchStatus(ctx, &promotion, kaprov1alpha1.PromotionPhasePaused,
			specHash, "Suspended", "Promotion or parent Kapro is suspended")
	}

	runSpec, err := buildRunSpec(&promotion, &parent)
	if err != nil {
		return r.patchUnresolved(ctx, &promotion, "BuildRunSpecFailed", err.Error())
	}

	// List existing attempts (PromotionRuns owned by this Promotion).
	var runs kaprov1alpha1.PromotionRunList
	if err := r.List(ctx, &runs, client.MatchingLabels{promotionOwnerLabel: promotion.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list owned PromotionRuns: %w", err)
	}
	sort.Slice(runs.Items, func(i, j int) bool {
		return runs.Items[i].CreationTimestamp.After(runs.Items[j].CreationTimestamp.Time)
	})

	// If the newest attempt matches the current spec hash, just mirror status.
	var activeRun *kaprov1alpha1.PromotionRun
	for i := range runs.Items {
		run := &runs.Items[i]
		if run.Labels[promotionSpecHashLabel] == specHash {
			activeRun = run
			break
		}
	}

	// priorTerminalExists drives the Docker-lifecycle Restarting phase:
	// a freshly-stamped attempt is "Restarting" iff at least one prior
	// attempt has reached a terminal phase (Succeeded/Failed/Superseded).
	priorTerminalExists := false
	for i := range runs.Items {
		if runs.Items[i].Status.Phase.IsTerminal() {
			priorTerminalExists = true
			break
		}
	}

	if activeRun == nil {
		// Spec changed (or first attempt). Supersede any non-terminal previous run.
		supersededNames, err := r.supersedePrevious(ctx, runs.Items)
		if err != nil {
			return ctrl.Result{}, err
		}
		newRun, err := r.stampAttempt(ctx, &promotion, runSpec, specHash)
		if err != nil {
			return ctrl.Result{}, err
		}
		activeRun = newRun
		r.Recorder.Eventf(&promotion, "Normal", "AttemptStamped",
			"Created PromotionRun %s for spec hash %s", newRun.Name, specHash)
		// Publish kapro.io/promotion.attempt.* CloudEvents for downstream
		// subscribers. These are attempt-scoped events that fire
		// regardless of whether the coarse phase also changed below.
		r.emitAttemptStamped(ctx, &promotion, newRun.Name)
		for _, name := range supersededNames {
			r.emitAttemptSuperseded(ctx, &promotion, name)
		}
	}

	return r.patchStatusFromRun(ctx, &promotion, activeRun, specHash, priorTerminalExists)
}

// emitAttemptStamped publishes a kapro.io/promotion.attempt.stamped
// CloudEvent. Nil-safe: a no-op when the controller runs without a
// Lifecycle dispatcher (e.g. older tests).
func (r *PromotionReconciler) emitAttemptStamped(ctx context.Context, p *kaprov1alpha1.Promotion, runName string) {
	publisher, ok := r.Lifecycle.(interface {
		PublishAttemptEvent(context.Context, *kaprov1alpha1.Promotion, string, string)
	})
	if !ok {
		return
	}
	publisher.PublishAttemptEvent(ctx, p, runName, "stamped")
}

func (r *PromotionReconciler) emitAttemptSuperseded(ctx context.Context, p *kaprov1alpha1.Promotion, runName string) {
	publisher, ok := r.Lifecycle.(interface {
		PublishAttemptEvent(context.Context, *kaprov1alpha1.Promotion, string, string)
	})
	if !ok {
		return
	}
	publisher.PublishAttemptEvent(ctx, p, runName, "superseded")
}

// stampAttempt creates a new PromotionRun for this Promotion attempt.
func (r *PromotionReconciler) stampAttempt(ctx context.Context, p *kaprov1alpha1.Promotion,
	spec kaprov1alpha1.PromotionRunSpec, specHash string) (*kaprov1alpha1.PromotionRun, error) {

	name := attemptName(p.Name, specHash)
	run := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				promotionOwnerLabel:    p.Name,
				promotionSpecHashLabel: specHash,
			},
			Annotations: copyStringMap(p.Annotations),
		},
		Spec: spec,
	}
	if err := controllerutil.SetControllerReference(p, run, r.Scheme); err != nil {
		return nil, fmt.Errorf("set Promotion owner on PromotionRun: %w", err)
	}
	if err := r.Create(ctx, run); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Deterministic name collision: fetch the existing one.
			if getErr := r.Get(ctx, client.ObjectKey{Name: name}, run); getErr != nil {
				return nil, fmt.Errorf("re-fetch PromotionRun after AlreadyExists: %w", getErr)
			}
			return run, nil
		}
		return nil, fmt.Errorf("create PromotionRun: %w", err)
	}
	return run, nil
}

// supersedePrevious marks any non-terminal PromotionRun owned by this
// Promotion as Superseded with reason SupersededByNewPromotionAttempt.
// Returns the names of runs it actually superseded so the caller can
// emit one kapro.io/promotion.attempt.superseded CloudEvent per name.
func (r *PromotionReconciler) supersedePrevious(ctx context.Context, runs []kaprov1alpha1.PromotionRun) ([]string, error) {
	now := metav1.Now()
	var superseded []string
	for i := range runs {
		run := &runs[i]
		if run.Status.Phase.IsTerminal() {
			continue
		}
		patch := client.MergeFrom(run.DeepCopy())
		run.Status.Phase = kaprov1alpha1.PromotionRunPhaseSuperseded
		run.Status.CompletedAt = now.Format(time.RFC3339)
		meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             supersededReason,
			Message:            "Newer PromotionRun was stamped for this Promotion intent",
			LastTransitionTime: now,
			ObservedGeneration: run.Generation,
		})
		if err := r.Status().Patch(ctx, run, patch); err != nil {
			return superseded, fmt.Errorf("supersede PromotionRun %s: %w", run.Name, err)
		}
		superseded = append(superseded, run.Name)
	}
	return superseded, nil
}

// suspendOwnedRuns flips spec.suspended=true on every owned non-terminal run.
func (r *PromotionReconciler) suspendOwnedRuns(ctx context.Context, p *kaprov1alpha1.Promotion) error {
	var runs kaprov1alpha1.PromotionRunList
	if err := r.List(ctx, &runs, client.MatchingLabels{promotionOwnerLabel: p.Name}); err != nil {
		return fmt.Errorf("list owned PromotionRuns: %w", err)
	}
	for i := range runs.Items {
		run := &runs.Items[i]
		if run.Spec.Suspended || run.Status.Phase.IsTerminal() {
			continue
		}
		patch := client.MergeFrom(run.DeepCopy())
		run.Spec.Suspended = true
		if err := r.Patch(ctx, run, patch); err != nil {
			return fmt.Errorf("suspend PromotionRun %s: %w", run.Name, err)
		}
	}
	return nil
}

func (r *PromotionReconciler) patchStatusFromRun(ctx context.Context, p *kaprov1alpha1.Promotion,
	run *kaprov1alpha1.PromotionRun, specHash string, priorTerminalExists bool) (ctrl.Result, error) {

	prevPhase := p.Status.Phase
	patch := client.MergeFrom(p.DeepCopy())

	startedAt := &run.CreationTimestamp
	if run.CreationTimestamp.IsZero() {
		startedAt = nil
	}
	var completedAt *metav1.Time
	if run.Status.Phase.IsTerminal() && run.Status.CompletedAt != "" {
		if t, err := time.Parse(time.RFC3339, run.Status.CompletedAt); err == nil {
			tt := metav1.NewTime(t)
			completedAt = &tt
		}
	}

	current := kaprov1alpha1.PromotionAttemptRef{
		Name:        run.Name,
		SpecHash:    specHash,
		Version:     run.Spec.Version,
		Phase:       run.Status.Phase,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
	}

	if !run.Status.Phase.IsTerminal() {
		p.Status.ActiveAttemptRef = &current
	} else {
		p.Status.ActiveAttemptRef = nil
	}

	p.Status.Attempts = upsertAttempt(p.Status.Attempts, current)
	p.Status.ResolvedVersion = run.Status.ResolvedVersion
	if p.Status.ResolvedVersion == "" {
		p.Status.ResolvedVersion = run.Spec.Version
	}
	p.Status.Phase = computePromotionPhase(run.Status.Phase, priorTerminalExists)
	p.Status.ObservedGeneration = p.Generation

	setPromotionConditions(p, run)
	r.emitPhaseTransitionEvent(p, prevPhase, run.Name)

	if err := r.Status().Patch(ctx, p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Promotion status: %w", err)
	}
	r.dispatchLifecycle(ctx, p, prevPhase, p.Status.Phase)
	return ctrl.Result{RequeueAfter: requeueForPhase(p.Status.Phase)}, nil
}

func (r *PromotionReconciler) patchStatus(ctx context.Context, p *kaprov1alpha1.Promotion,
	phase kaprov1alpha1.PromotionPhase, _ /*specHash retained for symmetry*/, reason, msg string) (ctrl.Result, error) {

	prevPhase := p.Status.Phase
	patch := client.MergeFrom(p.DeepCopy())
	p.Status.Phase = phase
	p.Status.ObservedGeneration = p.Generation
	if phase == kaprov1alpha1.PromotionPhasePaused || phase == kaprov1alpha1.PromotionPhaseTerminating {
		p.Status.ActiveAttemptRef = nil
	}
	condStatus := metav1.ConditionFalse
	if phase == kaprov1alpha1.PromotionPhaseSucceeded {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: p.Generation,
	})
	// Surface Suspended condition explicitly so users can filter on it.
	suspendedStatus := metav1.ConditionFalse
	if phase == kaprov1alpha1.PromotionPhasePaused {
		suspendedStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Suspended",
		Status:             suspendedStatus,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: p.Generation,
	})
	r.emitPhaseTransitionEvent(p, prevPhase, "")
	if err := r.Status().Patch(ctx, p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Promotion status: %w", err)
	}
	r.dispatchLifecycle(ctx, p, prevPhase, phase)
	return ctrl.Result{RequeueAfter: requeueForPhase(phase)}, nil
}

// dispatchLifecycle fans out user-declared spec.lifecycle.handlers when a
// coarse phase transition occurs. Nil-safe: when the controller has no
// dispatcher wired (legacy tests, test doubles), this is a no-op. The
// dispatcher itself is fire-and-forget — failures never propagate here.
func (r *PromotionReconciler) dispatchLifecycle(ctx context.Context,
	p *kaprov1alpha1.Promotion, prev, next kaprov1alpha1.PromotionPhase) {
	if r.Lifecycle == nil || prev == next {
		return
	}
	r.Lifecycle.OnPhaseTransition(ctx, p, prev, next)
}

// requeueForPhase returns the periodic requeue interval for the given coarse
// Promotion phase. Steady-state phases (Succeeded, Failed, Paused,
// Terminating) skip the periodic wake-up entirely — child PromotionRun
// watches drive reconciliation when meaningful state changes. Active phases
// keep the existing 15s cadence as a belt-and-braces for cases where a
// watch event is lost.
func requeueForPhase(phase kaprov1alpha1.PromotionPhase) time.Duration {
	switch phase {
	case kaprov1alpha1.PromotionPhaseSucceeded,
		kaprov1alpha1.PromotionPhaseFailed,
		kaprov1alpha1.PromotionPhasePaused,
		kaprov1alpha1.PromotionPhaseTerminating:
		return 0
	}
	return promotionIntentRequeue
}

func (r *PromotionReconciler) patchUnresolved(ctx context.Context, p *kaprov1alpha1.Promotion,
	reason, msg string) (ctrl.Result, error) {
	r.Recorder.Event(p, "Warning", reason, msg)
	return r.patchStatus(ctx, p, kaprov1alpha1.PromotionPhasePending, "", reason, msg)
}

// buildRunSpec derives a PromotionRunSpec from a Promotion + parent Kapro.
func buildRunSpec(p *kaprov1alpha1.Promotion, parent *kaprov1alpha1.Kapro) (kaprov1alpha1.PromotionRunSpec, error) {
	plans := append([]kaprov1alpha1.PromotionPlanRef(nil), p.Spec.PromotionPlans...)
	if len(plans) == 0 {
		// KaproReconciler materializes Kapro.spec.promotionplan as a separate
		// PromotionPlan CR named via InlinePromotionPlanName; reference that
		// generated object, not the bare Kapro name.
		plans = []kaprov1alpha1.PromotionPlanRef{{
			Name:          "inline",
			PromotionPlan: InlinePromotionPlanName(parent.Name),
		}}
	}
	if p.Spec.Version == "" && len(p.Spec.Versions) == 0 {
		return kaprov1alpha1.PromotionRunSpec{}, fmt.Errorf("either spec.version or spec.versions must be set")
	}
	return kaprov1alpha1.PromotionRunSpec{
		Version:        p.Spec.Version,
		Versions:       copyStringMap(p.Spec.Versions),
		PromotionPlans: plans,
		Scope:          p.Spec.Scope,
		Timeout:        p.Spec.Timeout,
		// Bug A fix: suspend state must propagate from Promotion intent to
		// the freshly stamped PromotionRun at t=0. Without this, a Promotion
		// created with spec.suspended=true would stamp a non-suspended run;
		// suspension would only take effect on the next reconcile cycle.
		Suspended: p.Spec.Suspended,
	}, nil
}

// promotionSpecHash is the deterministic hash of the Promotion spec used to
// detect drift and trigger a new attempt. Excludes Suspended (suspending
// should not create a new attempt) and includes the kaproRef so cross-fleet
// retargeting also stamps a fresh run.
func promotionSpecHash(s *kaprov1alpha1.PromotionSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "k=%s|", s.KaproRef)
	fmt.Fprintf(h, "v=%s|", s.Version)
	keys := make([]string, 0, len(s.Versions))
	for k := range s.Versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "u:%s=%s|", k, s.Versions[k])
	}
	for _, p := range s.PromotionPlans {
		fmt.Fprintf(h, "p:%s=%s|", p.Name, p.PromotionPlan)
	}
	if s.Scope != nil {
		scope := append([]string(nil), s.Scope.Targets...)
		sort.Strings(scope)
		for _, t := range scope {
			fmt.Fprintf(h, "s:%s|", t)
		}
	}
	fmt.Fprintf(h, "t=%s|", s.Timeout)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func attemptName(promotionName, specHash string) string {
	base := promotionName
	suffixLen := len(specHash) + 1 // "-<hash>"
	if len(base)+suffixLen > 63 {
		base = strings.TrimRight(base[:63-suffixLen], "-")
	}
	return base + "-" + specHash
}

// upsertAttempt replaces an existing entry with the same name+specHash
// (updating phase/timestamps), or prepends a new entry. The list is
// capped at MaxPromotionAttempts; older attempts fall off.
func upsertAttempt(list []kaprov1alpha1.PromotionAttemptRef, current kaprov1alpha1.PromotionAttemptRef) []kaprov1alpha1.PromotionAttemptRef {
	for i := range list {
		if list[i].Name == current.Name {
			// Preserve original StartedAt; update mutable fields.
			if list[i].StartedAt != nil {
				current.StartedAt = list[i].StartedAt
			}
			if current.SupersededReason == "" {
				current.SupersededReason = list[i].SupersededReason
			}
			list[i] = current
			return list
		}
	}
	out := append([]kaprov1alpha1.PromotionAttemptRef{current}, list...)
	if len(out) > kaprov1alpha1.MaxPromotionAttempts {
		out = out[:kaprov1alpha1.MaxPromotionAttempts]
	}
	return out
}

// computePromotionPhase maps the active PromotionRun phase plus contextual
// state into the Docker-lifecycle phase exposed on Promotion.status.
//
// A freshly-stamped attempt sitting in Pending while at least one earlier
// attempt has reached a terminal state is reported as Restarting (Docker's
// "restarting" between exited and running). Otherwise Pending bubbles up
// as Pending so first-attempt latency stays visible.
func computePromotionPhase(rp kaprov1alpha1.PromotionRunPhase, priorTerminalExists bool) kaprov1alpha1.PromotionPhase {
	switch rp {
	case kaprov1alpha1.PromotionRunPhaseComplete:
		return kaprov1alpha1.PromotionPhaseSucceeded
	case kaprov1alpha1.PromotionRunPhaseFailed:
		return kaprov1alpha1.PromotionPhaseFailed
	case kaprov1alpha1.PromotionRunPhaseProgressing:
		return kaprov1alpha1.PromotionPhaseProgressing
	case kaprov1alpha1.PromotionRunPhaseSuperseded:
		// The activeRun selection prefers the newest matching-hash run, so
		// this branch only fires when nothing newer exists for the current
		// hash — treat it as Pending pending a fresh stamp.
		return kaprov1alpha1.PromotionPhasePending
	case kaprov1alpha1.PromotionRunPhasePending, "":
		if priorTerminalExists {
			return kaprov1alpha1.PromotionPhaseRestarting
		}
		return kaprov1alpha1.PromotionPhasePending
	default:
		return kaprov1alpha1.PromotionPhase(rp)
	}
}

// setPromotionConditions writes the per-phase status conditions:
// Ready, Progressing, Suspended, Stalled (reserved), RollbackAvailable.
// Stalled is left unset here — it is owned by the PromotionRun timeout
// machinery and aggregated into Promotion by future work.
func setPromotionConditions(p *kaprov1alpha1.Promotion, run *kaprov1alpha1.PromotionRun) {
	phase := p.Status.Phase
	now := metav1.Now()

	ready := metav1.ConditionFalse
	readyReason := "Reconciled"
	readyMsg := fmt.Sprintf("Active attempt %s in phase %s", run.Name, run.Status.Phase)
	switch phase {
	case kaprov1alpha1.PromotionPhaseSucceeded:
		ready = metav1.ConditionTrue
		readyReason = "Succeeded"
		readyMsg = fmt.Sprintf("Attempt %s completed", run.Name)
	case kaprov1alpha1.PromotionPhaseFailed:
		readyReason = "Failed"
		readyMsg = fmt.Sprintf("Attempt %s failed", run.Name)
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             ready,
		Reason:             readyReason,
		Message:            readyMsg,
		LastTransitionTime: now,
		ObservedGeneration: p.Generation,
	})

	progressing := metav1.ConditionFalse
	progressingReason := "Idle"
	if phase == kaprov1alpha1.PromotionPhaseProgressing ||
		phase == kaprov1alpha1.PromotionPhaseRestarting ||
		phase == kaprov1alpha1.PromotionPhasePending {
		progressing = metav1.ConditionTrue
		progressingReason = "AttemptInFlight"
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Progressing",
		Status:             progressing,
		Reason:             progressingReason,
		Message:            string(phase),
		LastTransitionTime: now,
		ObservedGeneration: p.Generation,
	})

	suspended := metav1.ConditionFalse
	if phase == kaprov1alpha1.PromotionPhasePaused {
		suspended = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Suspended",
		Status:             suspended,
		Reason:             "SpecSuspended",
		Message:            fmt.Sprintf("spec.suspended=%t", p.Spec.Suspended),
		LastTransitionTime: now,
		ObservedGeneration: p.Generation,
	})

	// RollbackAvailable: at least one prior attempt has reached Succeeded
	// (observability — wires up to spec.rollbackTo once that field lands).
	rollback := metav1.ConditionFalse
	for _, a := range p.Status.Attempts {
		if a.Phase == kaprov1alpha1.PromotionRunPhaseComplete {
			rollback = metav1.ConditionTrue
			break
		}
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "RollbackAvailable",
		Status:             rollback,
		Reason:             "HistoricalSuccess",
		Message:            "at least one prior attempt reached Succeeded",
		LastTransitionTime: now,
		ObservedGeneration: p.Generation,
	})
}

// emitPhaseTransitionEvent records a Kubernetes Event whenever the coarse
// Promotion phase changes. Events are best-effort; the controller does not
// fail reconcile if the recorder buffer is full.
func (r *PromotionReconciler) emitPhaseTransitionEvent(p *kaprov1alpha1.Promotion,
	prevPhase kaprov1alpha1.PromotionPhase, runName string) {
	if prevPhase == p.Status.Phase {
		return
	}
	eventType := "Normal"
	switch p.Status.Phase {
	case kaprov1alpha1.PromotionPhaseFailed:
		eventType = "Warning"
	}
	reason := string(p.Status.Phase)
	msg := fmt.Sprintf("Promotion phase: %s -> %s", prevPhase, p.Status.Phase)
	if runName != "" {
		msg = fmt.Sprintf("%s (run %s)", msg, runName)
	}
	r.Recorder.Event(p, eventType, reason, msg)
}

// promotionKaproRefIndex is the field-indexer key on Promotion.spec.kaproRef.
// The PromotionController uses it to map a Kapro change event to the small
// set of Promotions that actually reference that fleet, instead of listing
// every Promotion and filtering in memory.
const promotionKaproRefIndex = "spec.kaproRef"

func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&kaprov1alpha1.Promotion{},
		promotionKaproRefIndex,
		func(obj client.Object) []string {
			p, ok := obj.(*kaprov1alpha1.Promotion)
			if !ok || p.Spec.KaproRef == "" {
				return nil
			}
			return []string{p.Spec.KaproRef}
		},
	); err != nil {
		return fmt.Errorf("index Promotion.spec.kaproRef: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Promotion{}).
		Owns(&kaprov1alpha1.PromotionRun{}).
		Watches(
			&kaprov1alpha1.Kapro{},
			handler.EnqueueRequestsFromMapFunc(r.promotionsForKapro),
		).
		Complete(r)
}

func (r *PromotionReconciler) promotionsForKapro(ctx context.Context, obj client.Object) []reconcile.Request {
	kapro, ok := obj.(*kaprov1alpha1.Kapro)
	if !ok {
		return nil
	}
	var list kaprov1alpha1.PromotionList
	if err := r.List(ctx, &list,
		client.MatchingFields{promotionKaproRefIndex: kapro.Name},
	); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, p := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: p.Name},
		})
	}
	return requests
}
