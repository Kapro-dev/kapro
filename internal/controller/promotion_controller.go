package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PromotionReconciler drives a single cluster through the gate pipeline.
//
// State machine:
//
//	Pending → HealthCheck → Soaking → MetricsCheck → WaitingApproval → Applying → Converged | Failed
type PromotionReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

func (r *PromotionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var promo kaprov1alpha1.Promotion
	if err := r.Get(ctx, req.NamespacedName, &promo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling Promotion", "name", promo.Name, "phase", promo.Status.Phase, "env", promo.Spec.EnvironmentRef)

	switch promo.Status.Phase {
	case "":
		return r.transitionTo(ctx, &promo, kaprov1alpha1.PromotionPhasePending)
	case kaprov1alpha1.PromotionPhasePending:
		return r.handlePending(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseHealthCheck:
		return r.handleHealthCheck(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseSoaking:
		return r.handleSoaking(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseMetricsCheck:
		return r.handleMetricsCheck(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseWaitingApproval:
		return r.handleWaitingApproval(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseApplying:
		return r.handleApplying(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseConverged, kaprov1alpha1.PromotionPhaseFailed:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *PromotionReconciler) handlePending(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	// Check cluster is reachable via ClusterRegistration heartbeat
	reg, err := r.getRegistration(ctx, promo.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if !isHeartbeatFresh(reg.Status.LastHeartbeat) {
		log.FromContext(ctx).Info("cluster heartbeat stale, waiting", "env", promo.Spec.EnvironmentRef)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseHealthCheck)
}

func (r *PromotionReconciler) handleHealthCheck(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	reg, err := r.getRegistration(ctx, promo.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if !reg.Status.Healthy || !reg.Status.FluxReady {
		log.FromContext(ctx).Info("cluster not healthy yet", "healthy", reg.Status.Healthy, "fluxReady", reg.Status.FluxReady)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	policy, err := r.getPolicy(ctx, promo.Spec.PolicyRef)
	if err != nil || policy == nil || policy.Spec.Gate.SoakTime == "" {
		// No soak — skip straight to metrics or apply
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
	}

	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseSoaking)
}

func (r *PromotionReconciler) handleSoaking(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, promo.Spec.PolicyRef)
	if err != nil || policy == nil {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
	}

	soakDuration, err := time.ParseDuration(policy.Spec.Gate.SoakTime)
	if err != nil {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
	}

	if promo.Status.StartedAt == "" {
		patch := client.MergeFrom(promo.DeepCopy())
		promo.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
		return ctrl.Result{RequeueAfter: soakDuration}, r.Status().Patch(ctx, promo, patch)
	}

	startedAt, _ := time.Parse(time.RFC3339, promo.Status.StartedAt)
	elapsed := time.Since(startedAt)
	if elapsed < soakDuration {
		remaining := soakDuration - elapsed
		log.FromContext(ctx).Info("soaking", "remaining", remaining)
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
}

func (r *PromotionReconciler) handleMetricsCheck(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, promo.Spec.PolicyRef)
	if err != nil || policy == nil || len(policy.Spec.Gate.Metrics) == 0 {
		return r.nextAfterMetrics(ctx, promo, policy)
	}

	// Metrics evaluation — currently passes through (implement Prometheus query in gate package)
	// TODO: integrate internal/gate/prometheus.go
	log.FromContext(ctx).Info("metrics check: gate package not yet wired, passing")

	return r.nextAfterMetrics(ctx, promo, policy)
}

func (r *PromotionReconciler) nextAfterMetrics(ctx context.Context, promo *kaprov1alpha1.Promotion, policy *kaprov1alpha1.PromotionPolicy) (ctrl.Result, error) {
	if policy != nil && policy.Spec.Approval != nil && policy.Spec.Approval.Required {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseWaitingApproval)
	}
	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseApplying)
}

func (r *PromotionReconciler) handleWaitingApproval(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	// Look for an Approval object for this promotion
	var approvalList kaprov1alpha1.ApprovalList
	if err := r.List(ctx, &approvalList, client.MatchingLabels{
		"kapro.io/release":     promo.Spec.ReleaseRef,
		"kapro.io/environment": promo.Spec.EnvironmentRef,
	}); err != nil {
		return ctrl.Result{}, err
	}

	for _, approval := range approvalList.Items {
		if approval.Spec.Kind == kaprov1alpha1.ApprovalKindPromotion &&
			approval.Spec.EnvironmentRef == promo.Spec.EnvironmentRef {
			log.FromContext(ctx).Info("approval received", "approvedBy", approval.Spec.ApprovedBy, "bypass", approval.Spec.Bypass)
			return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseApplying)
		}
	}

	// No approval yet
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PromotionReconciler) handleApplying(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get the Environment to find actuator config
	var env kaprov1alpha1.Environment
	if err := r.Get(ctx, client.ObjectKey{Name: promo.Spec.EnvironmentRef}, &env); err != nil {
		return ctrl.Result{}, fmt.Errorf("environment %s not found: %w", promo.Spec.EnvironmentRef, err)
	}

	// Apply via actuator (Flux: mutate OCIRepository tag)
	if env.Spec.Actuator.Type == "flux" && env.Spec.Actuator.Flux != nil {
		// TODO: wire FluxActuator.Apply() here
		log.Info("applying version via Flux actuator",
			"env", promo.Spec.EnvironmentRef,
			"version", promo.Spec.Version,
			"ociRepo", env.Spec.Actuator.Flux.OCIRepository,
		)
	}

	// Poll ClusterRegistration for convergence
	reg, err := r.getRegistration(ctx, promo.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if reg.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		reg.Status.CurrentVersion == promo.Spec.Version {
		log.Info("cluster converged", "env", promo.Spec.EnvironmentRef, "version", promo.Spec.Version)
		patch := client.MergeFrom(promo.DeepCopy())
		promo.Status.Phase = kaprov1alpha1.PromotionPhaseConverged
		promo.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		return ctrl.Result{}, r.Status().Patch(ctx, promo, patch)
	}

	if reg.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
		return ctrl.Result{}, r.failPromotion(ctx, promo,
			fmt.Sprintf("cluster %s reported Failed phase", promo.Spec.EnvironmentRef))
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PromotionReconciler) transitionTo(ctx context.Context, promo *kaprov1alpha1.Promotion, phase kaprov1alpha1.PromotionPhase) (ctrl.Result, error) {
	patch := client.MergeFrom(promo.DeepCopy())
	promo.Status.Phase = phase
	if phase == kaprov1alpha1.PromotionPhasePending && promo.Status.StartedAt == "" {
		promo.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return ctrl.Result{Requeue: true}, r.Status().Patch(ctx, promo, patch)
}

func (r *PromotionReconciler) failPromotion(ctx context.Context, promo *kaprov1alpha1.Promotion, msg string) error {
	patch := client.MergeFrom(promo.DeepCopy())
	promo.Status.Phase = kaprov1alpha1.PromotionPhaseFailed
	promo.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	promo.Status.Message = msg
	promo.Status.Conditions = append(promo.Status.Conditions, metav1.Condition{
		Type:               "Failed",
		Status:             metav1.ConditionTrue,
		Reason:             "GateFailed",
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	return r.Status().Patch(ctx, promo, patch)
}

func (r *PromotionReconciler) getRegistration(ctx context.Context, envRef string) (*kaprov1alpha1.ClusterRegistration, error) {
	var regList kaprov1alpha1.ClusterRegistrationList
	if err := r.List(ctx, &regList, client.MatchingLabels{
		"kapro.io/environment": envRef,
	}); err != nil {
		return nil, err
	}
	for _, reg := range regList.Items {
		if reg.Spec.EnvironmentRef == envRef {
			return &reg, nil
		}
	}
	return nil, fmt.Errorf("no ClusterRegistration found for environment %s", envRef)
}

func (r *PromotionReconciler) getPolicy(ctx context.Context, policyRef string) (*kaprov1alpha1.PromotionPolicy, error) {
	if policyRef == "" {
		return nil, nil
	}
	var policy kaprov1alpha1.PromotionPolicy
	if err := r.Get(ctx, client.ObjectKey{Name: policyRef}, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func isHeartbeatFresh(lastHeartbeat string) bool {
	if lastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, lastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < 2*time.Minute
}

func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Promotion{}).
		Complete(r)
}
