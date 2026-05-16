package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const promotionIntentRequeue = 15 * time.Second

// PromotionReconciler turns desired promotion intent into a PromotionRun and
// mirrors run status back to the intent object.
type PromotionReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=promotionpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns/status,verbs=get

func (r *PromotionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var promotion kaprov1alpha1.Promotion
	if err := r.Get(ctx, req.NamespacedName, &promotion); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !promotion.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	spec := promotionRunSpecFromPromotion(&promotion)
	policyDecision, err := r.evaluatePromotionPolicies(ctx, &promotion, time.Now().UTC())
	if err != nil {
		return ctrl.Result{}, err
	}
	if !policyDecision.Allowed {
		if err := r.suspendOwnedPromotionRuns(ctx, &promotion, spec); err != nil {
			return ctrl.Result{}, fmt.Errorf("suspend PromotionRuns for policy denial: %w", err)
		}
		if err := r.patchPromotionPolicyDecision(ctx, &promotion, policyDecision); err != nil {
			return ctrl.Result{}, err
		}
		if policyDecision.RequeueAfter > 0 {
			return ctrl.Result{RequeueAfter: policyDecision.RequeueAfter}, nil
		}
		return ctrl.Result{}, nil
	}

	runName := promotionRunName(&promotion, spec)

	var run kaprov1alpha1.PromotionRun
	if err := r.Get(ctx, client.ObjectKey{Name: runName}, &run); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		legacyRun, adopted, err := r.adoptOrSuspendLegacyPromotionRun(ctx, &promotion, spec)
		if err != nil {
			return ctrl.Result{}, err
		}
		if adopted {
			run = *legacyRun
			runName = run.Name
		} else {
			run = kaprov1alpha1.PromotionRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:        runName,
					Labels:      copyStringMap(promotion.Labels),
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
			return ctrl.Result{RequeueAfter: promotionIntentRequeue}, nil
		}
	}

	patch := client.MergeFrom(run.DeepCopy())
	run.Labels = copyStringMap(promotion.Labels)
	run.Annotations = copyStringMap(promotion.Annotations)
	run.Spec = spec
	if err := controllerutil.SetControllerReference(&promotion, &run, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("set Promotion owner on PromotionRun: %w", err)
	}
	if err := r.Patch(ctx, &run, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch PromotionRun: %w", err)
	}

	statusPatch := client.MergeFrom(promotion.DeepCopy())
	promotion.Status.ActiveRun = runName
	promotion.Status.LastRun = runName
	promotion.Status.ResolvedVersion = run.Status.ResolvedVersion
	promotion.Status.Phase = promotionPhaseFromRun(run.Status.Phase)
	promotion.Status.ObservedGeneration = promotion.Generation
	promotion.Status.Conditions = run.Status.Conditions
	if len(promotion.Status.Conditions) == 0 {
		meta.SetStatusCondition(&promotion.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "PromotionRunPending",
			Message:            "PromotionRun has been created and is waiting for execution status",
			ObservedGeneration: promotion.Generation,
		})
	}
	if err := r.Status().Patch(ctx, &promotion, statusPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Promotion status: %w", err)
	}
	return ctrl.Result{RequeueAfter: promotionIntentRequeue}, nil
}

func promotionRunSpecFromPromotion(promotion *kaprov1alpha1.Promotion) kaprov1alpha1.PromotionRunSpec {
	return kaprov1alpha1.PromotionRunSpec{
		Version:        promotionVersion(promotion),
		Versions:       copyStringMap(promotion.Spec.Versions),
		PromotionPlans: append([]kaprov1alpha1.PromotionPlanRef(nil), promotion.Spec.PromotionPlans...),
		Suspended:      promotion.Spec.Suspended,
		Scope:          promotion.Spec.Scope,
		Timeout:        promotion.Spec.Timeout,
	}
}

func (r *PromotionReconciler) adoptOrSuspendLegacyPromotionRun(ctx context.Context, promotion *kaprov1alpha1.Promotion, spec kaprov1alpha1.PromotionRunSpec) (*kaprov1alpha1.PromotionRun, bool, error) {
	var legacy kaprov1alpha1.PromotionRun
	if err := r.Get(ctx, client.ObjectKey{Name: legacyPromotionRunName(promotion)}, &legacy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if promotionRunImmutableSpecEqual(legacy.Spec, spec) {
		return &legacy, true, nil
	}
	if err := r.suspendPromotionRun(ctx, &legacy); err != nil {
		return nil, false, fmt.Errorf("suspend legacy PromotionRun %s: %w", legacy.Name, err)
	}
	return nil, false, nil
}

func (r *PromotionReconciler) suspendOwnedPromotionRuns(ctx context.Context, promotion *kaprov1alpha1.Promotion, spec kaprov1alpha1.PromotionRunSpec) error {
	names := map[string]struct{}{
		legacyPromotionRunName(promotion): {},
		promotionRunName(promotion, spec): {},
	}
	if promotion.Status.ActiveRun != "" {
		names[promotion.Status.ActiveRun] = struct{}{}
	}
	if promotion.Status.LastRun != "" {
		names[promotion.Status.LastRun] = struct{}{}
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := r.List(ctx, &runs); err != nil {
		return err
	}
	for i := range runs.Items {
		run := &runs.Items[i]
		if promotionControlsRun(promotion, run) {
			names[run.Name] = struct{}{}
		}
	}

	for name := range names {
		var run kaprov1alpha1.PromotionRun
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &run); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if !promotionMayOwnRun(promotion, &run, name, names) {
			continue
		}
		if err := r.suspendPromotionRun(ctx, &run); err != nil {
			return fmt.Errorf("suspend PromotionRun %s: %w", run.Name, err)
		}
	}
	return nil
}

func promotionMayOwnRun(promotion *kaprov1alpha1.Promotion, run *kaprov1alpha1.PromotionRun, name string, candidateNames map[string]struct{}) bool {
	if promotionControlsRun(promotion, run) {
		return true
	}
	if _, ok := candidateNames[name]; !ok {
		return false
	}
	return name == promotion.Status.ActiveRun ||
		name == promotion.Status.LastRun ||
		name == legacyPromotionRunName(promotion) ||
		strings.HasPrefix(name, strings.TrimRight(promotion.Name, "-.")+"-run-")
}

func promotionControlsRun(promotion *kaprov1alpha1.Promotion, run *kaprov1alpha1.PromotionRun) bool {
	for _, ref := range run.OwnerReferences {
		if ref.APIVersion != "kapro.io/v1alpha1" || ref.Kind != "Promotion" || ref.Name != promotion.Name {
			continue
		}
		return promotion.UID == "" || ref.UID == promotion.UID
	}
	return false
}

func (r *PromotionReconciler) suspendPromotionRun(ctx context.Context, run *kaprov1alpha1.PromotionRun) error {
	if run.Spec.Suspended {
		return nil
	}
	patch := client.MergeFrom(run.DeepCopy())
	run.Spec.Suspended = true
	return r.Patch(ctx, run, patch)
}

func promotionRunImmutableSpecEqual(a, b kaprov1alpha1.PromotionRunSpec) bool {
	return a.Version == b.Version &&
		reflect.DeepEqual(a.Versions, b.Versions) &&
		reflect.DeepEqual(a.PromotionPlans, b.PromotionPlans) &&
		reflect.DeepEqual(a.Scope, b.Scope)
}

func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Promotion{}).
		Complete(r)
}

func promotionVersion(promotion *kaprov1alpha1.Promotion) string {
	if promotion.Spec.Version != "" {
		return promotion.Spec.Version
	}
	if promotion.Spec.Artifact == nil {
		return ""
	}
	if promotion.Spec.Artifact.Version != "" {
		return promotion.Spec.Artifact.Version
	}
	if promotion.Spec.Artifact.Repository != "" && promotion.Spec.Artifact.Digest != "" {
		return promotion.Spec.Artifact.Repository + "@" + promotion.Spec.Artifact.Digest
	}
	if promotion.Spec.Artifact.Image != "" && promotion.Spec.Artifact.Tag != "" {
		return promotion.Spec.Artifact.Image + ":" + promotion.Spec.Artifact.Tag
	}
	return promotion.Spec.Artifact.Tag
}

func promotionRunName(promotion *kaprov1alpha1.Promotion, spec kaprov1alpha1.PromotionRunSpec) string {
	identity := struct {
		Version        string                           `json:"version,omitempty"`
		Versions       map[string]string                `json:"versions,omitempty"`
		PromotionPlans []kaprov1alpha1.PromotionPlanRef `json:"promotionplans,omitempty"`
		Scope          *kaprov1alpha1.PromotionRunScope `json:"scope,omitempty"`
	}{
		Version:        spec.Version,
		Versions:       spec.Versions,
		PromotionPlans: spec.PromotionPlans,
		Scope:          spec.Scope,
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		payload = []byte(fmt.Sprintf("%#v", identity))
	}
	sum := sha256.Sum256(payload)
	suffix := "-run-" + hex.EncodeToString(sum[:])[:12]
	base := strings.TrimRight(promotion.Name, "-.")
	if len(base)+len(suffix) > 253 {
		base = strings.TrimRight(base[:253-len(suffix)], "-.")
	}
	return base + suffix
}

func legacyPromotionRunName(promotion *kaprov1alpha1.Promotion) string {
	base := strings.TrimRight(promotion.Name, "-.")
	suffix := "-run-1"
	if len(base)+len(suffix) > 253 {
		base = strings.TrimRight(base[:253-len(suffix)], "-.")
	}
	return base + suffix
}

func promotionPhaseFromRun(phase kaprov1alpha1.PromotionRunPhase) kaprov1alpha1.PromotionPhase {
	switch phase {
	case kaprov1alpha1.PromotionRunPhaseComplete:
		return kaprov1alpha1.PromotionPhasePromoted
	case kaprov1alpha1.PromotionRunPhaseFailed:
		return kaprov1alpha1.PromotionPhaseFailed
	case kaprov1alpha1.PromotionRunPhaseProgressing:
		return kaprov1alpha1.PromotionPhaseRunning
	default:
		return kaprov1alpha1.PromotionPhasePending
	}
}
