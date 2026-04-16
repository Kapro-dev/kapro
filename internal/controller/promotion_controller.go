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
	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	"kapro.io/kapro/internal/gate"
	crdprovider "kapro.io/kapro/internal/provider/crd"
)

// PromotionReconciler drives a single cluster through the gate pipeline.
//
// State machine:
//
//	Pending → HealthCheck → Soaking → MetricsCheck → WaitingApproval → Applying → Converged | Failed
type PromotionReconciler struct {
	client.Client
	Actuator     *fluxactuator.FluxActuator
	Provider     *crdprovider.CRDProvider
	SoakGate     *gate.SoakGate
	MetricsGate  *gate.MetricsGate
	ApprovalGate *gate.ApprovalGate
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

	if !reg.Status.Health.AllWorkloadsReady {
		log.FromContext(ctx).Info("cluster not healthy yet", "healthy", reg.Status.Health.AllWorkloadsReady, "deliverySystem", reg.Status.DeliverySystem)
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

	// Initialise soak clock on first entry.
	if promo.Status.StartedAt == "" {
		patch := client.MergeFrom(promo.DeepCopy())
		promo.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
		if err := r.Status().Patch(ctx, promo, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	g := r.SoakGate
	if g == nil {
		g = &gate.SoakGate{}
	}

	result, err := g.Evaluate(ctx, gate.Request{Promotion: promo, Policy: policy})
	if err != nil {
		return ctrl.Result{}, err
	}

	log.FromContext(ctx).Info("soak gate", "passed", result.Passed, "message", result.Message)

	if result.Passed {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
	}

	after := parseDurationOrDefault(result.RetryAfter, 30*time.Second)
	return ctrl.Result{RequeueAfter: after}, nil
}

func (r *PromotionReconciler) handleMetricsCheck(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, promo.Spec.PolicyRef)
	if err != nil || policy == nil || len(policy.Spec.Gate.Metrics) == 0 {
		return r.nextAfterMetrics(ctx, promo, policy)
	}

	g := r.MetricsGate
	if g == nil {
		g = &gate.MetricsGate{}
	}

	// Evaluate each metric in order; block on the first failure.
	for i := range policy.Spec.Gate.Metrics {
		result, err := g.Evaluate(ctx, gate.Request{Promotion: promo, Policy: policy, MetricIndex: i})
		if err != nil {
			// Non-fatal: log and retry — don't fail the promotion on transient errors.
			log.FromContext(ctx).Error(err, "metrics gate error, will retry", "index", i)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		log.FromContext(ctx).Info("metrics gate", "index", i, "passed", result.Passed, "message", result.Message)
		if !result.Passed {
			after := parseDurationOrDefault(result.RetryAfter, 30*time.Second)
			return ctrl.Result{RequeueAfter: after}, nil
		}
	}

	return r.nextAfterMetrics(ctx, promo, policy)
}

func (r *PromotionReconciler) nextAfterMetrics(ctx context.Context, promo *kaprov1alpha1.Promotion, policy *kaprov1alpha1.PromotionPolicy) (ctrl.Result, error) {
	if policy != nil && policy.Spec.Approval != nil && policy.Spec.Approval.Required {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseWaitingApproval)
	}
	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseApplying)
}

func (r *PromotionReconciler) handleWaitingApproval(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	g := r.ApprovalGate
	if g == nil {
		g = &gate.ApprovalGate{Client: r.Client}
	}

	policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
	result, err := g.Evaluate(ctx, gate.Request{Promotion: promo, Policy: policy})
	if err != nil {
		return ctrl.Result{}, err
	}

	log.FromContext(ctx).Info("approval gate", "passed", result.Passed, "message", result.Message)

	if result.Passed {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseApplying)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PromotionReconciler) handleApplying(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var env kaprov1alpha1.Environment
	if err := r.Get(ctx, client.ObjectKey{Name: promo.Spec.EnvironmentRef}, &env); err != nil {
		return ctrl.Result{}, fmt.Errorf("environment %s not found: %w", promo.Spec.EnvironmentRef, err)
	}

	// Apply: set ClusterRegistration.spec.desiredVersion.
	// The cluster-controller on the workload cluster will pick this up and patch OCIRepository.
	if r.Actuator != nil && env.Spec.Actuator.Type == "flux" && env.Spec.Actuator.Flux != nil {
		if err := r.Actuator.Apply(ctx, fluxactuator.ApplyRequest{
			Environment:     &env,
			Version:         promo.Spec.Version,
			PreviousVersion: promo.Status.PreviousVersion,
		}); err != nil {
			log.Error(err, "FluxActuator.Apply failed, will retry")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		log.Info("FluxActuator.Apply succeeded — waiting for cluster convergence",
			"env", promo.Spec.EnvironmentRef,
			"version", promo.Spec.Version,
			"ociRepo", env.Spec.Actuator.Flux.OCIRepository,
		)
	}

	// Poll ClusterRegistration for convergence (set by cluster-controller).
	reg, err := r.getRegistration(ctx, promo.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if reg.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		reg.Status.CurrentVersions["ocs"] == promo.Spec.Version {
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

	log.Info("waiting for convergence",
		"env", promo.Spec.EnvironmentRef,
		"clusterPhase", reg.Status.Phase,
		"currentVersion", reg.Status.CurrentVersions["ocs"],
		"wantVersion", promo.Spec.Version,
	)
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

func parseDurationOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}
