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
		return ctrl.Result{}, nil
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
		return r.patchStatus(ctx, &promotion, kaprov1alpha1.PromotionPhaseSuspended,
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

	if activeRun == nil {
		// Spec changed (or first attempt). Supersede any non-terminal previous run.
		if err := r.supersedePrevious(ctx, runs.Items); err != nil {
			return ctrl.Result{}, err
		}
		newRun, err := r.stampAttempt(ctx, &promotion, runSpec, specHash)
		if err != nil {
			return ctrl.Result{}, err
		}
		activeRun = newRun
		r.Recorder.Eventf(&promotion, "Normal", "AttemptStamped",
			"Created PromotionRun %s for spec hash %s", newRun.Name, specHash)
	}

	return r.patchStatusFromRun(ctx, &promotion, activeRun, specHash)
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
func (r *PromotionReconciler) supersedePrevious(ctx context.Context, runs []kaprov1alpha1.PromotionRun) error {
	now := metav1.Now()
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
			return fmt.Errorf("supersede PromotionRun %s: %w", run.Name, err)
		}
	}
	return nil
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
	run *kaprov1alpha1.PromotionRun, specHash string) (ctrl.Result, error) {

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
	p.Status.Phase = promotionPhaseFromRun(run.Status.Phase)
	p.Status.ObservedGeneration = p.Generation

	condStatus := metav1.ConditionFalse
	condReason := "Reconciled"
	condMsg := fmt.Sprintf("Active attempt %s in phase %s", run.Name, run.Status.Phase)
	if p.Status.Phase == kaprov1alpha1.PromotionPhasePromoted {
		condStatus = metav1.ConditionTrue
		condReason = "Promoted"
		condMsg = fmt.Sprintf("Attempt %s completed", run.Name)
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		Reason:             condReason,
		Message:            condMsg,
		ObservedGeneration: p.Generation,
	})

	if err := r.Status().Patch(ctx, p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Promotion status: %w", err)
	}
	return ctrl.Result{RequeueAfter: promotionIntentRequeue}, nil
}

func (r *PromotionReconciler) patchStatus(ctx context.Context, p *kaprov1alpha1.Promotion,
	phase kaprov1alpha1.PromotionPhase, specHash, reason, msg string) (ctrl.Result, error) {

	patch := client.MergeFrom(p.DeepCopy())
	p.Status.Phase = phase
	p.Status.ObservedGeneration = p.Generation
	if phase == kaprov1alpha1.PromotionPhaseSuspended {
		p.Status.ActiveAttemptRef = nil
	}
	condStatus := metav1.ConditionFalse
	if phase == kaprov1alpha1.PromotionPhasePromoted {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: p.Generation,
	})
	if err := r.Status().Patch(ctx, p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Promotion status: %w", err)
	}
	return ctrl.Result{RequeueAfter: promotionIntentRequeue}, nil
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
		plans = []kaprov1alpha1.PromotionPlanRef{{
			Name:          "inline",
			PromotionPlan: parent.Name,
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

func promotionPhaseFromRun(rp kaprov1alpha1.PromotionRunPhase) kaprov1alpha1.PromotionPhase {
	switch rp {
	case kaprov1alpha1.PromotionRunPhaseComplete:
		return kaprov1alpha1.PromotionPhasePromoted
	case kaprov1alpha1.PromotionRunPhaseFailed:
		return kaprov1alpha1.PromotionPhaseFailed
	case kaprov1alpha1.PromotionRunPhaseProgressing:
		return kaprov1alpha1.PromotionPhaseRunning
	case kaprov1alpha1.PromotionRunPhaseSuperseded:
		// Superseded runs themselves are terminal but the parent Promotion
		// stays Running while a newer attempt is in flight; the outer
		// reconcile selects the newest matching-hash run as activeRun so
		// this branch is rare (only fires when nothing newer exists).
		return kaprov1alpha1.PromotionPhasePending
	case kaprov1alpha1.PromotionRunPhasePending, "":
		return kaprov1alpha1.PromotionPhasePending
	default:
		return kaprov1alpha1.PromotionPhase(rp)
	}
}

func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, p := range list.Items {
		if p.Spec.KaproRef == kapro.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: p.Name},
			})
		}
	}
	return requests
}
