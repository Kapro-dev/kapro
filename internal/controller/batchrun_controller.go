package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	crdprovider "kapro.io/kapro/internal/provider/crd"
)

// BatchRunReconciler drives one batch of clusters through apply + convergence + gate.
//
// State machine:
//
//	Pending → Resolving → Applying → WaitingConvergence → GateCheck → WaitingApproval → Complete | Failed
type BatchRunReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Actuator *fluxactuator.FluxActuator
	Provider *crdprovider.CRDProvider
}

// +kubebuilder:rbac:groups=kapro.io,resources=batchruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=batchruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

func (r *BatchRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var br kaprov1alpha1.BatchRun
	if err := r.Get(ctx, req.NamespacedName, &br); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling BatchRun", "name", br.Name, "phase", br.Status.Phase, "batch", br.Spec.BatchName)

	switch br.Status.Phase {
	case "":
		return r.setBatchPhase(ctx, &br, kaprov1alpha1.BatchPhasePending)
	case kaprov1alpha1.BatchPhasePending:
		return r.handlePending(ctx, &br)
	case kaprov1alpha1.BatchPhaseResolving:
		return r.handleResolving(ctx, &br)
	case kaprov1alpha1.BatchPhaseApplying:
		return r.handleApplying(ctx, &br)
	case kaprov1alpha1.BatchPhaseWaitingConvergence:
		return r.handleWaitingConvergence(ctx, &br)
	case kaprov1alpha1.BatchPhaseGateCheck:
		return r.handleGateCheck(ctx, &br)
	case kaprov1alpha1.BatchPhaseWaitingApproval:
		return r.handleWaitingApproval(ctx, &br)
	case kaprov1alpha1.BatchPhaseComplete, kaprov1alpha1.BatchPhaseFailed:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *BatchRunReconciler) handlePending(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	// Check dependent batches are complete
	for _, dep := range br.Spec.DependsOn {
		var depBR kaprov1alpha1.BatchRunList
		if err := r.List(ctx, &depBR, client.MatchingLabels{
			"kapro.io/release": br.Spec.ReleaseRef,
			"kapro.io/batch":   dep,
		}); err != nil {
			return ctrl.Result{}, err
		}
		for _, d := range depBR.Items {
			if d.Status.Phase != kaprov1alpha1.BatchPhaseComplete {
				log.FromContext(ctx).Info("waiting for dependency", "dep", dep, "phase", d.Status.Phase)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}
	}
	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseResolving)
}

func (r *BatchRunReconciler) handleResolving(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Resolve all label selectors to concrete Environment names
	resolved := []kaprov1alpha1.ClusterStatus{}

	for _, sel := range br.Spec.Selectors {
		selector, err := metav1.LabelSelectorAsSelector(&sel)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("invalid batch selector: %w", err)
		}

		var envList kaprov1alpha1.EnvironmentList
		if err := r.List(ctx, &envList, &client.ListOptions{LabelSelector: selector}); err != nil {
			return ctrl.Result{}, err
		}

		for _, env := range envList.Items {
			resolved = append(resolved, kaprov1alpha1.ClusterStatus{
				EnvironmentRef: env.Name,
				Phase:          kaprov1alpha1.ClusterPhasePending,
			})
		}
	}

	if len(resolved) == 0 {
		log.Info("no clusters resolved for batch — completing", "batch", br.Spec.BatchName)
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
	}

	patch := client.MergeFrom(br.DeepCopy())
	br.Status.Clusters = resolved
	br.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	if err := r.Status().Patch(ctx, br, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("resolved batch clusters", "count", len(resolved))
	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseApplying)
}

func (r *BatchRunReconciler) handleApplying(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get release to find version
	var release kaprov1alpha1.Release
	if err := r.Get(ctx, client.ObjectKey{Name: br.Spec.ReleaseRef, Namespace: br.Namespace}, &release); err != nil {
		return ctrl.Result{}, fmt.Errorf("release %s not found: %w", br.Spec.ReleaseRef, err)
	}

	version := release.Spec.Artifact
	var applyErrors []string

	// Apply version to all clusters in this batch in parallel (fire and forget — convergence checked next phase).
	for _, cs := range br.Status.Clusters {
		var env kaprov1alpha1.Environment
		if err := r.Get(ctx, client.ObjectKey{Name: cs.EnvironmentRef}, &env); err != nil {
			log.Error(err, "environment not found, skipping", "env", cs.EnvironmentRef)
			applyErrors = append(applyErrors, fmt.Sprintf("%s: environment not found", cs.EnvironmentRef))
			continue
		}

		if r.Actuator != nil && env.Spec.Actuator.Type == "flux" && env.Spec.Actuator.Flux != nil {
			if err := r.Actuator.Apply(ctx, fluxactuator.ApplyRequest{
				Environment: &env,
				Version:     version,
			}); err != nil {
				log.Error(err, "FluxActuator.Apply failed", "env", cs.EnvironmentRef)
				applyErrors = append(applyErrors, fmt.Sprintf("%s: %v", cs.EnvironmentRef, err))
				continue
			}
			log.Info("FluxActuator.Apply succeeded",
				"env", cs.EnvironmentRef,
				"version", version,
				"ociRepo", env.Spec.Actuator.Flux.OCIRepository,
			)
		}
	}

	if len(applyErrors) == len(br.Status.Clusters) {
		return ctrl.Result{}, r.failBatch(ctx, br, fmt.Sprintf("all cluster applies failed: %v", applyErrors))
	}

	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseWaitingConvergence)
}

func (r *BatchRunReconciler) handleWaitingConvergence(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var release kaprov1alpha1.Release
	if err := r.Get(ctx, client.ObjectKey{Name: br.Spec.ReleaseRef, Namespace: br.Namespace}, &release); err != nil {
		return ctrl.Result{}, err
	}
	targetVersion := release.Spec.Artifact

	allConverged := true
	anyFailed := false
	updatedClusters := make([]kaprov1alpha1.ClusterStatus, len(br.Status.Clusters))

	for i, cs := range br.Status.Clusters {
		updatedClusters[i] = cs

		var regList kaprov1alpha1.ClusterRegistrationList
		if err := r.List(ctx, &regList, client.MatchingLabels{
			"kapro.io/environment": cs.EnvironmentRef,
		}); err != nil {
			allConverged = false
			continue
		}

		for _, reg := range regList.Items {
			if reg.Spec.EnvironmentRef != cs.EnvironmentRef {
				continue
			}
			updatedClusters[i].Phase = reg.Status.Phase
			updatedClusters[i].Version = reg.Status.CurrentVersions["ocs"]

			if reg.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
				reg.Status.CurrentVersions["ocs"] == targetVersion {
				log.Info("cluster converged", "env", cs.EnvironmentRef, "version", targetVersion)
			} else if reg.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
				anyFailed = true
				updatedClusters[i].Message = "cluster reported Failed"
			} else {
				allConverged = false
			}
		}
	}

	// Persist updated cluster statuses
	patch := client.MergeFrom(br.DeepCopy())
	br.Status.Clusters = updatedClusters
	if err := r.Status().Patch(ctx, br, patch); err != nil {
		return ctrl.Result{}, err
	}

	if anyFailed {
		return ctrl.Result{}, r.failBatch(ctx, br, "one or more clusters failed to converge")
	}

	if !allConverged {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseGateCheck)
}

func (r *BatchRunReconciler) handleGateCheck(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, br.Spec.PolicyRef)
	if err != nil || policy == nil {
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
	}

	// Metrics gate
	if len(policy.Spec.Gate.Metrics) > 0 {
		// TODO: wire gate/prometheus.go
		log.FromContext(ctx).Info("gate check: metrics gate not yet wired, passing")
	}

	// Manual approval required?
	if policy.Spec.Approval != nil && policy.Spec.Approval.Required {
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseWaitingApproval)
	}

	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
}

func (r *BatchRunReconciler) handleWaitingApproval(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	var approvalList kaprov1alpha1.ApprovalList
	if err := r.List(ctx, &approvalList, client.MatchingLabels{
		"kapro.io/release": br.Spec.ReleaseRef,
		"kapro.io/batch":   br.Spec.BatchName,
	}); err != nil {
		return ctrl.Result{}, err
	}

	for _, approval := range approvalList.Items {
		if approval.Spec.Kind == kaprov1alpha1.ApprovalKindBatch &&
			approval.Spec.Ref == br.Spec.BatchName {
			log.FromContext(ctx).Info("batch approval received",
				"approvedBy", approval.Spec.ApprovedBy,
				"bypass", approval.Spec.Bypass,
			)
			r.Recorder.Event(br, corev1.EventTypeNormal, "ApprovalReceived", "Batch approval received")
			return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
		}
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *BatchRunReconciler) setBatchPhase(ctx context.Context, br *kaprov1alpha1.BatchRun, phase kaprov1alpha1.BatchPhase) (ctrl.Result, error) {
	patch := client.MergeFrom(br.DeepCopy())
	br.Status.Phase = phase
	if phase == kaprov1alpha1.BatchPhaseComplete || phase == kaprov1alpha1.BatchPhaseFailed {
		br.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r.Recorder.Event(br, corev1.EventTypeNormal, "PhaseTransition", fmt.Sprintf("→ %s", phase))
	return ctrl.Result{Requeue: true}, r.Status().Patch(ctx, br, patch)
}

func (r *BatchRunReconciler) failBatch(ctx context.Context, br *kaprov1alpha1.BatchRun, msg string) error {
	patch := client.MergeFrom(br.DeepCopy())
	br.Status.Phase = kaprov1alpha1.BatchPhaseFailed
	br.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	br.Status.Conditions = append(br.Status.Conditions, metav1.Condition{
		Type:               "Failed",
		Status:             metav1.ConditionTrue,
		Reason:             "ConvergenceFailed",
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	r.Recorder.Event(br, corev1.EventTypeWarning, "Failed", msg)
	return r.Status().Patch(ctx, br, patch)
}

func (r *BatchRunReconciler) getPolicy(ctx context.Context, policyRef string) (*kaprov1alpha1.PromotionPolicy, error) {
	if policyRef == "" {
		return nil, nil
	}
	var policy kaprov1alpha1.PromotionPolicy
	if err := r.Get(ctx, client.ObjectKey{Name: policyRef}, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func (r *BatchRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.BatchRun{}).
		Complete(r)
}
