package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

const promotionIntentRequeue = 15 * time.Second

// PromotionReconciler materializes Promotion intent into PromotionRun
// attempts and mirrors run status back into Promotion.status.
type PromotionReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns/status,verbs=get
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

	if promotion.Spec.Suspended || parent.Spec.Suspended {
		if err := r.suspendOwnedRuns(ctx, &promotion); err != nil {
			return ctrl.Result{}, err
		}
		return r.patchStatus(ctx, &promotion, kaprov1alpha1.PromotionPhaseSuspended,
			"", "", "Suspended", "Promotion or parent Kapro is suspended")
	}

	spec, err := buildRunSpec(&promotion, &parent)
	if err != nil {
		return r.patchUnresolved(ctx, &promotion, "BuildRunSpecFailed", err.Error())
	}

	runName := promotionRunName(&promotion)
	var run kaprov1alpha1.PromotionRun
	getErr := r.Get(ctx, client.ObjectKey{Name: runName}, &run)
	switch {
	case apierrors.IsNotFound(getErr):
		run = kaprov1alpha1.PromotionRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:        runName,
				Labels:      promotionRunLabels(&promotion),
				Annotations: copyStringMap(promotion.Annotations),
			},
			Spec: spec,
		}
		if err := controllerutil.SetControllerReference(&promotion, &run, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("set Promotion owner on PromotionRun: %w", err)
		}
		if err := r.Create(ctx, &run); err != nil {
			return ctrl.Result{}, fmt.Errorf("create PromotionRun: %w", err)
		}
		r.Recorder.Eventf(&promotion, "Normal", "AttemptCreated",
			"Created PromotionRun %s for version %s", runName, spec.Version)
	case getErr != nil:
		return ctrl.Result{}, getErr
	default:
		patch := client.MergeFrom(run.DeepCopy())
		run.Labels = promotionRunLabels(&promotion)
		run.Spec = spec
		if err := r.Patch(ctx, &run, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch PromotionRun: %w", err)
		}
	}

	return r.patchStatus(ctx, &promotion,
		promotionPhaseFromRun(run.Status.Phase),
		runName, runName,
		"Reconciled", "PromotionRun is the active attempt")
}

func (r *PromotionReconciler) patchStatus(ctx context.Context, p *kaprov1alpha1.Promotion,
	phase kaprov1alpha1.PromotionPhase, activeRun, lastRun, reason, msg string) (ctrl.Result, error) {

	patch := client.MergeFrom(p.DeepCopy())
	p.Status.Phase = phase
	p.Status.ActiveRun = activeRun
	if lastRun != "" {
		p.Status.LastRun = lastRun
	}
	p.Status.ObservedGeneration = p.Generation
	if activeRun != "" {
		p.Status.AttemptCount = max32(p.Status.AttemptCount, 1)
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
	return r.patchStatus(ctx, p, kaprov1alpha1.PromotionPhasePending, "", "", reason, msg)
}

func (r *PromotionReconciler) suspendOwnedRuns(ctx context.Context, p *kaprov1alpha1.Promotion) error {
	var runs kaprov1alpha1.PromotionRunList
	if err := r.List(ctx, &runs, client.MatchingLabels{promotionOwnerLabel: p.Name}); err != nil {
		return fmt.Errorf("list owned PromotionRuns: %w", err)
	}
	for i := range runs.Items {
		run := &runs.Items[i]
		if run.Spec.Suspended {
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

// buildRunSpec derives a PromotionRunSpec from a Promotion + parent Kapro.
// PromotionPlans override comes from Promotion.spec.promotionPlans; when
// unset we synthesise a single ref from Kapro's inline plan so the runtime
// has at least one entry (PromotionRunSpec.PromotionPlans has MinItems=1).
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

const promotionOwnerLabel = "kapro.io/promotion"

func promotionRunLabels(p *kaprov1alpha1.Promotion) map[string]string {
	labels := copyStringMap(p.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[promotionOwnerLabel] = p.Name
	return labels
}

func promotionRunName(p *kaprov1alpha1.Promotion) string {
	// Deterministic: <promotion-name>-<short-spec-hash>. The hash covers
	// version + versions + plans + scope so a spec edit produces a fresh run.
	hash := shortPromotionSpecHash(&p.Spec)
	base := p.Name
	if len(base)+9 > 63 {
		base = base[:54]
		base = strings.TrimRight(base, "-")
	}
	return base + "-" + hash
}

func shortPromotionSpecHash(s *kaprov1alpha1.PromotionSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "v=%s|", s.Version)
	for k, v := range s.Versions {
		fmt.Fprintf(h, "u:%s=%s|", k, v)
	}
	for _, p := range s.PromotionPlans {
		fmt.Fprintf(h, "p:%s=%s|", p.Name, p.PromotionPlan)
	}
	if s.Scope != nil {
		for _, t := range s.Scope.Targets {
			fmt.Fprintf(h, "s:%s|", t)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

func promotionPhaseFromRun(rp kaprov1alpha1.PromotionRunPhase) kaprov1alpha1.PromotionPhase {
	switch rp {
	case kaprov1alpha1.PromotionRunPhaseComplete:
		return kaprov1alpha1.PromotionPhasePromoted
	case kaprov1alpha1.PromotionRunPhaseFailed:
		return kaprov1alpha1.PromotionPhaseFailed
	case kaprov1alpha1.PromotionRunPhaseProgressing:
		return kaprov1alpha1.PromotionPhaseRunning
	case kaprov1alpha1.PromotionRunPhasePending, "":
		return kaprov1alpha1.PromotionPhasePending
	default:
		return kaprov1alpha1.PromotionPhase(rp)
	}
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
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
