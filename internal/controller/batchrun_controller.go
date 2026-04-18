package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/oci"
)

// BatchRunReconciler orchestrates one batch of clusters using the Job->Pod
// ownership model: BatchRun creates one Promotion per resolved cluster and
// watches until all reach Converged. The Promotion state machine owns the full
// gate->apply->converge cycle. BatchRun never calls the Actuator directly.
//
// State machine:
//
//	Pending -> Resolving -> WaitingPromotions -> GateCheck -> WaitingApproval -> Complete | Failed
type BatchRunReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	SoakGate     gate.Gate
	MetricsGate  gate.Gate
	ApprovalGate gate.Gate
	// KedaGate evaluates KEDA-provider metrics (provider == "keda").
	// When nil, KEDA metrics are skipped (pass-through).
	KedaGate gate.Gate
	// MLflowGate evaluates MLflow Model Registry metrics (provider == "mlflow").
	// When nil, MLflow metrics are skipped (pass-through).
	MLflowGate gate.Gate
	// OCIService enables artifact inspection operations.
	OCIService oci.Service
}

// +kubebuilder:rbac:groups=kapro.io,resources=batchruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=batchruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=batchruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

const batchRunFinalizer = "kapro.io/batchrun-cleanup"

func (r *BatchRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var br kaprov1alpha1.BatchRun
	if err := r.Get(ctx, req.NamespacedName, &br); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling BatchRun", "name", br.Name, "phase", br.Status.Phase, "batch", br.Spec.BatchName)

	// Handle deletion: fail the BatchRun so its parent Pipeline doesn't block.
	if !br.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&br, batchRunFinalizer) {
			if br.Status.Phase != kaprov1alpha1.BatchPhaseFailed &&
				br.Status.Phase != kaprov1alpha1.BatchPhaseComplete {
				_ = r.failBatch(ctx, &br, "BatchRun deleted before completion")
			}
			controllerutil.RemoveFinalizer(&br, batchRunFinalizer)
			if err := r.Update(ctx, &br); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove batchrun finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer before first state change.
	if !controllerutil.ContainsFinalizer(&br, batchRunFinalizer) {
		controllerutil.AddFinalizer(&br, batchRunFinalizer)
		if err := r.Update(ctx, &br); err != nil {
			return ctrl.Result{}, fmt.Errorf("add batchrun finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	switch br.Status.Phase {
	case "":
		return r.setBatchPhase(ctx, &br, kaprov1alpha1.BatchPhasePending)
	case kaprov1alpha1.BatchPhasePending:
		return r.handlePending(ctx, &br)
	case kaprov1alpha1.BatchPhaseResolving:
		return r.handleResolving(ctx, &br)
	case kaprov1alpha1.BatchPhaseWaitingPromotions:
		return r.handleWaitingPromotions(ctx, &br)
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
	// Check dependent batches are complete.
	// Use IndexKeyRelease field index (registered by ReleaseReconciler.SetupWithManager)
	// for O(log n) lookup instead of a full label scan.
	for _, dep := range br.Spec.DependsOn {
		var depBR kaprov1alpha1.BatchRunList
		if err := r.List(ctx, &depBR,
			client.InNamespace(br.Namespace),
			client.MatchingFields{IndexKeyRelease: br.Spec.ReleaseRef},
			client.Limit(500),
		); err != nil {
			return ctrl.Result{}, fmt.Errorf("list BatchRuns for dependency %s: %w", dep, err)
		}
		for _, d := range depBR.Items {
			if d.Spec.BatchName != dep {
				continue
			}
			if d.Status.Phase != kaprov1alpha1.BatchPhaseComplete {
				log.FromContext(ctx).Info("waiting for dependency", "dep", dep, "phase", d.Status.Phase)
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
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
		if err := r.List(ctx, &envList, &client.ListOptions{LabelSelector: selector, Limit: 500}); err != nil {
			return ctrl.Result{}, fmt.Errorf("list Environments for selector: %w", err)
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
		return ctrl.Result{}, fmt.Errorf("patch resolved clusters: %w", err)
	}

	log.Info("resolved batch clusters", "count", len(resolved))
	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseWaitingPromotions)
}

// handleWaitingPromotions implements the Job->Pod ownership model.
//
// Creates one owned Promotion per cluster (idempotent by name). Mirrors each
// Promotion phase into BatchRun.Status.Clusters. When all Promotions are
// Converged, advances to GateCheck. Any Promotion failure fails the batch.
func (r *BatchRunReconciler) handleWaitingPromotions(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var release kaprov1alpha1.Release
	if err := r.Get(ctx, client.ObjectKey{Name: br.Spec.ReleaseRef, Namespace: br.Namespace}, &release); err != nil {
		return ctrl.Result{}, fmt.Errorf("get Release %s: %w", br.Spec.ReleaseRef, err)
	}

	// OwnerReference: Promotion is owned by BatchRun, same as Pod owned by Job.
	ownerRef := metav1.OwnerReference{
		APIVersion:         kaprov1alpha1.GroupVersion.String(),
		Kind:               "BatchRun",
		Name:               br.Name,
		UID:                br.UID,
		BlockOwnerDeletion: boolPtr(true),
		Controller:         boolPtr(true),
	}

	allConverged := true
	anyFailed := false
	updatedClusters := make([]kaprov1alpha1.ClusterStatus, len(br.Status.Clusters))
	var promotionRefs []string

	for i, cs := range br.Status.Clusters {
		updatedClusters[i] = cs

		// Deterministic name: <batchrun>-<env> for easy correlation in kubectl.
		promoName := fmt.Sprintf("%s-%s", br.Name, cs.EnvironmentRef)
		promotionRefs = append(promotionRefs, promoName)

		var promo kaprov1alpha1.Promotion
		err := r.Get(ctx, client.ObjectKey{Name: promoName, Namespace: br.Namespace}, &promo)

		if errors.IsNotFound(err) {
			newPromo := kaprov1alpha1.Promotion{
				ObjectMeta: metav1.ObjectMeta{
					Name:      promoName,
					Namespace: br.Namespace,
					Labels: map[string]string{
						"kapro.io/release":     br.Spec.ReleaseRef,
						"kapro.io/environment": cs.EnvironmentRef,
						"kapro.io/batch":       br.Spec.BatchName,
						"kapro.io/batchrun":    br.Name,
					},
					OwnerReferences: []metav1.OwnerReference{ownerRef},
				},
				Spec: kaprov1alpha1.PromotionSpec{
					ReleaseRef:     br.Spec.ReleaseRef,
					EnvironmentRef: cs.EnvironmentRef,
					Version:        release.Status.ResolvedVersion,
					PolicyRef:      coalesceString(br.Spec.PromotionPolicyRef, br.Spec.PolicyRef),
					AppKey:         resolveAppKey(&release),
				},
			}
			if createErr := r.Create(ctx, &newPromo); createErr != nil {
				return ctrl.Result{}, fmt.Errorf("create Promotion %s: %w", promoName, createErr)
			}
			log.Info("created Promotion for cluster", "promotion", promoName, "env", cs.EnvironmentRef)
			allConverged = false
			continue
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("get Promotion %s: %w", promoName, err)
		}

		// Mirror Promotion phase -> ClusterStatus (same as Job mirrors Pod phase).
		updatedClusters[i].Version = promo.Spec.Version
		switch promo.Status.Phase {
		case kaprov1alpha1.PromotionPhaseConverged:
			updatedClusters[i].Phase = kaprov1alpha1.ClusterPhaseConverged
		case kaprov1alpha1.PromotionPhaseFailed:
			updatedClusters[i].Phase = kaprov1alpha1.ClusterPhaseFailed
			updatedClusters[i].Message = promo.Status.Message
			anyFailed = true
		default:
			updatedClusters[i].Phase = kaprov1alpha1.ClusterPhase(promo.Status.Phase)
			allConverged = false
		}
	}

	patch := client.MergeFrom(br.DeepCopy())
	br.Status.Clusters = updatedClusters
	br.Status.PromotionRefs = promotionRefs
	if err := r.Status().Patch(ctx, br, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch cluster statuses: %w", err)
	}

	if anyFailed {
		return ctrl.Result{}, r.failBatch(ctx, br, "one or more cluster Promotions failed")
	}
	if !allConverged {
		// Owned Promotion changes re-trigger via Owns() watch -- no polling needed.
		return ctrl.Result{}, nil
	}

	log.Info("all cluster Promotions converged -- advancing to GateCheck", "batch", br.Spec.BatchName)
	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseGateCheck)
}

func (r *BatchRunReconciler) handleGateCheck(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	// Prefer ProgressionPolicy (batch-level gate) if set.
	// Fall back to legacy PromotionPolicy via PolicyRef for backward compatibility
	// — emit a deprecation event so operators can migrate.
	progressionPolicyRef := br.Spec.ProgressionPolicyRef
	if progressionPolicyRef != "" {
		return r.handleProgressionGateCheck(ctx, br, progressionPolicyRef)
	}

	// Legacy path: PromotionPolicy used as a batch gate.
	legacyRef := coalesceString(br.Spec.PolicyRef)
	if legacyRef == "" {
		// No gate configured — advance immediately.
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
	}
	r.Recorder.Event(br, corev1.EventTypeWarning, "Deprecated",
		"batch policyRef uses PromotionPolicy as a batch gate; migrate to progressionPolicyRef + ProgressionPolicy")
	return r.handleLegacyPromotionPolicyGateCheck(ctx, br, legacyRef)
}

// handleProgressionGateCheck evaluates a ProgressionPolicy against the batch.
// This is the canonical batch-level gate path — semantically distinct from
// per-cluster PromotionPolicy gates.
func (r *BatchRunReconciler) handleProgressionGateCheck(ctx context.Context, br *kaprov1alpha1.BatchRun, policyRef string) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var pp kaprov1alpha1.ProgressionPolicy
	if err := r.Get(ctx, client.ObjectKey{Name: policyRef}, &pp); err != nil {
		log.Error(err, "ProgressionPolicy not found — advancing", "policyRef", policyRef)
		r.Recorder.Event(br, corev1.EventTypeWarning, "PolicyNotFound",
			fmt.Sprintf("ProgressionPolicy %q not found — advancing batch", policyRef))
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
	}

	// Batch soak — measured from when the last cluster converged (br.Status.StartedAt).
	if pp.Spec.BatchSoak != "" {
		soakDuration, err := time.ParseDuration(pp.Spec.BatchSoak)
		if err != nil {
			log.Error(err, "invalid batchSoak duration — skipping soak gate", "batchSoak", pp.Spec.BatchSoak)
		} else if br.Status.StartedAt != "" {
			if start, parseErr := time.Parse(time.RFC3339, br.Status.StartedAt); parseErr == nil {
				remaining := soakDuration - time.Since(start)
				if remaining > 0 {
					log.Info("batch soak pending", "remaining", remaining.Round(time.Second).String())
					r.Recorder.Event(br, corev1.EventTypeNormal, "GatePending",
						fmt.Sprintf("BatchSoak: %s remaining", remaining.Round(time.Second)))
					return ctrl.Result{RequeueAfter: remaining}, nil
				}
			}
		}
		r.Recorder.Event(br, corev1.EventTypeNormal, "GatePassed", "BatchSoak complete")
	}

	// Aggregate metrics — evaluate each metric across the batch.
	if len(pp.Spec.Metrics) > 0 {
		// Build a synthetic Promotion for gate.Request (reuses metric gate primitives).
		// The synthetic object carries only the fields metric gates need.
		syntheticPromo := &kaprov1alpha1.Promotion{
			Spec:   kaprov1alpha1.PromotionSpec{ReleaseRef: br.Spec.ReleaseRef},
			Status: kaprov1alpha1.PromotionStatus{StartedAt: br.Status.StartedAt},
		}
		// Wrap spec in a synthetic PromotionPolicy for the gate.Request interface.
		syntheticPolicy := &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{Metrics: pp.Spec.Metrics},
			},
		}
		for i, metric := range pp.Spec.Metrics {
			var g gate.Gate
			switch metric.Provider {
			case "keda":
				g = r.KedaGate
			case "mlflow":
				g = r.MLflowGate
			default:
				g = r.MetricsGate
			}
			if g == nil {
				log.Info("metric gate provider not configured — passing through", "provider", metric.Provider, "index", i)
				continue
			}
			result, err := g.Evaluate(ctx, gate.Request{
				Promotion:   syntheticPromo,
				Policy:      syntheticPolicy,
				MetricIndex: i,
			})
			if err != nil {
				log.Error(err, "batch metrics gate error, retrying", "index", i)
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
			}
			if !result.Passed {
				log.Info("batch metrics gate not yet passing", "index", i, "message", result.Message)
				r.Recorder.Event(br, corev1.EventTypeNormal, "GatePending",
					fmt.Sprintf("BatchMetrics[%d]: %s", i, result.Message))
				onFailure := pp.Spec.OnFailure
				if onFailure == kaprov1alpha1.ProgressionOnFailureHalt || onFailure == "" {
					return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
				}
				if onFailure == kaprov1alpha1.ProgressionOnFailureRetry {
					return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
				}
				// Skip: log and continue.
				r.Recorder.Event(br, corev1.EventTypeWarning, "GateSkipped",
					fmt.Sprintf("BatchMetrics[%d] failed, OnFailure=Skip", i))
			} else {
				r.Recorder.Event(br, corev1.EventTypeNormal, "GatePassed",
					fmt.Sprintf("BatchMetrics[%d]: %s", i, result.Message))
			}
		}
	}

	// Manual approval required?
	if pp.Spec.Approval != nil && pp.Spec.Approval.Required {
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseWaitingApproval)
	}

	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
}

// handleLegacyPromotionPolicyGateCheck is the backward-compat path for batches
// that still reference a PromotionPolicy via the deprecated policyRef field.
func (r *BatchRunReconciler) handleLegacyPromotionPolicyGateCheck(ctx context.Context, br *kaprov1alpha1.BatchRun, policyRef string) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	policy, err := r.getPolicy(ctx, policyRef)
	if err != nil || policy == nil {
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
	}

	syntheticPromo := &kaprov1alpha1.Promotion{
		Spec: kaprov1alpha1.PromotionSpec{
			ReleaseRef: br.Spec.ReleaseRef,
			PolicyRef:  policyRef,
		},
		Status: kaprov1alpha1.PromotionStatus{
			StartedAt: br.Status.StartedAt,
		},
	}

	if policy.Spec.Gate.SoakTime != "" && r.SoakGate != nil {
		result, err := r.SoakGate.Evaluate(ctx, gate.Request{Promotion: syntheticPromo, Policy: policy})
		if err != nil {
			return ctrl.Result{RequeueAfter: requeueNormal}, nil
		}
		if !result.Passed {
			log.Info("batch soak gate pending", "message", result.Message)
			r.Recorder.Event(br, corev1.EventTypeNormal, "GatePending", "SoakGate: "+result.Message)
			return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
		}
		r.Recorder.Event(br, corev1.EventTypeNormal, "GatePassed", "SoakGate: "+result.Message)
	}

	if len(policy.Spec.Gate.Metrics) > 0 {
		for i, metric := range policy.Spec.Gate.Metrics {
			var g gate.Gate
			switch metric.Provider {
			case "keda":
				g = r.KedaGate
			case "mlflow":
				g = r.MLflowGate
			default:
				g = r.MetricsGate
			}
			if g == nil {
				continue
			}
			result, err := g.Evaluate(ctx, gate.Request{Promotion: syntheticPromo, Policy: policy, MetricIndex: i})
			if err != nil {
				log.Error(err, "metrics gate error, retrying", "index", i)
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
			}
			if !result.Passed {
				r.Recorder.Event(br, corev1.EventTypeWarning, "GateFailed", result.Message)
				return ctrl.Result{RequeueAfter: parseDurationOrDefault(result.RetryAfter)}, nil
			}
			r.Recorder.Event(br, corev1.EventTypeNormal, "GatePassed", fmt.Sprintf("MetricsGate[%d]: %s", i, result.Message))
		}
	}

	if policy.Spec.Approval != nil && policy.Spec.Approval.Required {
		return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseWaitingApproval)
	}

	return r.setBatchPhase(ctx, br, kaprov1alpha1.BatchPhaseComplete)
}

func (r *BatchRunReconciler) handleWaitingApproval(ctx context.Context, br *kaprov1alpha1.BatchRun) (ctrl.Result, error) {
	var approvalList kaprov1alpha1.ApprovalList
	if err := r.List(ctx, &approvalList,
		client.InNamespace(br.Namespace),
		client.MatchingFields{IndexKeyRelease: br.Spec.ReleaseRef},
		client.Limit(500),
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Approvals: %w", err)
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

	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func (r *BatchRunReconciler) setBatchPhase(ctx context.Context, br *kaprov1alpha1.BatchRun, phase kaprov1alpha1.BatchPhase) (ctrl.Result, error) {
	patch := client.MergeFrom(br.DeepCopy())
	br.Status.Phase = phase
	br.Status.ObservedGeneration = br.Generation
	if phase == kaprov1alpha1.BatchPhaseComplete || phase == kaprov1alpha1.BatchPhaseFailed {
		br.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if phase == kaprov1alpha1.BatchPhaseComplete {
		apimeta.SetStatusCondition(&br.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Complete",
			ObservedGeneration: br.Generation,
			Message:            "all clusters converged",
			LastTransitionTime: metav1.Now(),
		})
		if br.Status.StartedAt != "" {
			if start, err := time.Parse(time.RFC3339, br.Status.StartedAt); err == nil {
				kaprometrics.BatchDuration.WithLabelValues(br.Spec.ReleaseRef).Observe(time.Since(start).Seconds())
			}
		}
		kaprometrics.WaveProgress.WithLabelValues(br.Spec.ReleaseRef, br.Spec.BatchName).Inc()
	}
	r.Recorder.Event(br, corev1.EventTypeNormal, "PhaseTransition", fmt.Sprintf("→ %s", phase))
	return ctrl.Result{Requeue: true}, r.Status().Patch(ctx, br, patch)
}

func (r *BatchRunReconciler) failBatch(ctx context.Context, br *kaprov1alpha1.BatchRun, msg string) error {
	patch := client.MergeFrom(br.DeepCopy())
	br.Status.Phase = kaprov1alpha1.BatchPhaseFailed
	br.Status.ObservedGeneration = br.Generation
	br.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	br.Status.Conditions = nil // clear stale conditions before SetStatusCondition
	apimeta.SetStatusCondition(&br.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "PromotionFailed",
		ObservedGeneration: br.Generation,
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

// coalesceString returns the first non-empty string from the arguments.
func coalesceString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// boolPtr returns a pointer to b -- used for OwnerReference fields.
func boolPtr(b bool) *bool { return &b }

func (r *BatchRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		For(&kaprov1alpha1.BatchRun{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		// Owns Promotions: any Promotion phase change re-triggers BatchRun reconcile,
		// same as a Job being re-triggered when one of its Pods changes phase.
		Owns(&kaprov1alpha1.Promotion{}).
		// Watch Approvals so that a batch-level manual approval immediately wakes up
		// the BatchRun stuck in WaitingApproval — without this watch it would only
		// advance on the next RequeueAfter tick.
		Watches(
			&kaprov1alpha1.Approval{},
			handler.EnqueueRequestsFromMapFunc(r.batchRunForApproval),
		).
		Complete(r)
}

// batchRunForApproval maps an Approval object to the BatchRun it unblocks.
// BatchRun names follow the convention: <release>-<batchName>.
// Approval.spec.kind == "Batch" and spec.ref is the batch name.
func (r *BatchRunReconciler) batchRunForApproval(ctx context.Context, obj client.Object) []ctrl.Request {
	approval, ok := obj.(*kaprov1alpha1.Approval)
	if !ok {
		return nil
	}
	if approval.Spec.Kind != kaprov1alpha1.ApprovalKindBatch {
		return nil
	}
	batchRunName := approval.Spec.Release + "-" + approval.Spec.Ref
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: batchRunName, Namespace: approval.Namespace}}}
}
