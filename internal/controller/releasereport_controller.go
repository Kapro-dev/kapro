package controller

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ReleaseReportReconciler maintains a live ReleaseReport for each Release.
//
// A ReleaseReport is created automatically when a Release is created and
// deleted when the Release is deleted. It aggregates:
//   - Overall phase, artifact, version, timing
//   - Per-pipeline breakdown: environments targeted, synced, failed, active stage
//   - Gate evaluation results across all Syncs
//   - Pending Approval CRs blocking progression
//
// The controller is purely observational — it never creates Syncs or modifies
// Release objects. It only reads Release, Sync, and Approval objects and writes
// ReleaseReport.Status. This makes it safe to enable/disable without affecting
// delivery.
//
// Design inspired by Flux Operator's FluxReport but scoped to per-Release
// delivery rather than per-cluster fleet state.
type ReleaseReportReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=releasereports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=releasereports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=syncs,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

func (r *ReleaseReportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// ReleaseReport name == Release name — one-to-one.
	var release kaprov1alpha1.Release
	if err := r.Get(ctx, req.NamespacedName, &release); err != nil {
		if errors.IsNotFound(err) {
			// Release gone — delete the report if it exists.
			return ctrl.Result{}, r.deleteReport(ctx, req.Name, req.Namespace)
		}
		return ctrl.Result{}, fmt.Errorf("get release: %w", err)
	}

	// Ensure ReleaseReport exists; create it if missing.
	report := &kaprov1alpha1.ReleaseReport{}
	err := r.Get(ctx, req.NamespacedName, report)
	if errors.IsNotFound(err) {
		report = &kaprov1alpha1.ReleaseReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      release.Name,
				Namespace: release.Namespace,
				Labels: map[string]string{
					"kapro.io/release": release.Name,
				},
			},
			Spec: kaprov1alpha1.ReleaseReportSpec{
				ReleaseRef: release.Name,
			},
		}
		if setErr := controllerutil.SetControllerReference(&release, report, r.Client.Scheme()); setErr != nil {
			return ctrl.Result{}, fmt.Errorf("set owner ref on ReleaseReport: %w", setErr)
		}
		if createErr := r.Create(ctx, report); createErr != nil {
			return ctrl.Result{}, fmt.Errorf("create ReleaseReport: %w", createErr)
		}
		log.Info("created ReleaseReport", "name", report.Name)
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("get ReleaseReport: %w", err)
	}

	// Collect all Syncs for this Release.
	// Sync is cluster-scoped — do NOT filter by namespace.
	var syncList kaprov1alpha1.SyncList
	if err := r.List(ctx, &syncList,
		client.MatchingLabels{"kapro.io/release": release.Name},
		client.Limit(2000),
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Syncs: %w", err)
	}

	// Collect pending Approvals.
	var approvalList kaprov1alpha1.ApprovalList
	if err := r.List(ctx, &approvalList,
		client.MatchingLabels{"kapro.io/release": release.Name},
		client.Limit(100),
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Approvals: %w", err)
	}

	// Build the updated status.
	newStatus := r.buildStatus(&release, syncList.Items, approvalList.Items)
	newStatus.ObservedGeneration = report.Generation

	patch := client.MergeFrom(report.DeepCopy())
	report.Status = newStatus
	if err := r.Status().Patch(ctx, report, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch ReleaseReport status: %w", err)
	}

	// Requeue every 30s while the Release is active so timing fields stay fresh.
	if release.Status.Phase == kaprov1alpha1.ReleasePhaseProgressing ||
		release.Status.Phase == kaprov1alpha1.ReleasePhasePending ||
		release.Status.Phase == "" {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// buildStatus aggregates all observed resources into a ReleaseReportStatus.
func (r *ReleaseReportReconciler) buildStatus(
	release *kaprov1alpha1.Release,
	syncs []kaprov1alpha1.Sync,
	approvals []kaprov1alpha1.Approval,
) kaprov1alpha1.ReleaseReportStatus {
	now := time.Now().UTC()

	st := kaprov1alpha1.ReleaseReportStatus{
		Phase:           release.Status.Phase,
		Artifact:        release.Spec.Artifact,
		ResolvedVersion: release.Status.ResolvedVersion,
		StartedAt:       release.Status.StartedAt,
		CompletedAt:     release.Status.CompletedAt,
	}

	// Compute elapsed duration.
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

	// Count environments across all Syncs (de-duplicated by environment name).
	// Rollback Syncs (kapro.io/rollback: "true") are counted separately.
	envPhases := make(map[string]kaprov1alpha1.SyncPhase, len(syncs))
	var rolledBack int
	for _, s := range syncs {
		if s.Labels["kapro.io/rollback"] == "true" {
			rolledBack++
			continue
		}
		// Last write wins — if an env has multiple Syncs (unusual), use most recent.
		envPhases[s.Spec.EnvironmentRef] = s.Status.Phase
	}

	var totalEnvs, synced, failed, pending int
	for _, phase := range envPhases {
		totalEnvs++
		switch phase {
		case kaprov1alpha1.SyncPhaseConverged:
			synced++
		case kaprov1alpha1.SyncPhaseFailed:
			failed++
		default:
			pending++
		}
	}

	st.TotalEnvironments = totalEnvs
	st.SyncedEnvironments = synced
	st.FailedEnvironments = failed
	st.PendingEnvironments = pending
	st.RolledBackEnvironments = rolledBack

	// Build per-pipeline reports from PipelineProgress.
	envReports := make([]kaprov1alpha1.EnvironmentReport, 0, len(syncs))
	seen := make(map[string]bool)
	for _, s := range syncs {
		if s.Labels["kapro.io/rollback"] == "true" {
			continue
		}
		envRef := s.Spec.EnvironmentRef
		if seen[envRef] {
			continue
		}
		seen[envRef] = true
		envReports = append(envReports, kaprov1alpha1.EnvironmentReport{
			Name:        envRef,
			Phase:       string(s.Status.Phase),
			PipelineRef: s.Labels["kapro.io/pipeline-ref"],
			Stage:       s.Spec.Stage,
			Version:     s.Spec.Version,
			SyncedAt:    s.Status.FinishedAt,
		})
	}
	st.Environments = envReports

	// Build gate reports from Sync gate statuses.
	gateReports := make([]kaprov1alpha1.GateReport, 0)
	for _, s := range syncs {
		if s.Spec.PolicyRef == "" {
			continue
		}
		var result string
		switch s.Status.Phase {
		case kaprov1alpha1.SyncPhaseConverged:
			result = "Passed"
		case kaprov1alpha1.SyncPhaseFailed:
			result = "Failed"
		case kaprov1alpha1.SyncPhaseMetricsCheck, kaprov1alpha1.SyncPhaseSoaking,
			kaprov1alpha1.SyncPhaseVerification, kaprov1alpha1.SyncPhaseHealthCheck:
			result = "Running"
		default:
			result = "Pending"
		}
		gateReports = append(gateReports, kaprov1alpha1.GateReport{
			Type:        s.Spec.PolicyRef,
			PipelineRef: s.Labels["kapro.io/pipeline-ref"],
			Stage:       s.Spec.Stage,
			Environment: s.Spec.EnvironmentRef,
			Result:      result,
		})
	}
	st.Gates = gateReports

	// Pending approvals.
	pendingApprovals := make([]string, 0)
	for _, a := range approvals {
		if a.Spec.Kind == kaprov1alpha1.ApprovalKindStage || a.Spec.Kind == kaprov1alpha1.ApprovalKindSync {
			pendingApprovals = append(pendingApprovals, a.Name)
		}
	}
	st.PendingApprovals = pendingApprovals

	// Set summary condition.
	var condStatus metav1.ConditionStatus
	var condReason, condMsg string
	switch release.Status.Phase {
	case kaprov1alpha1.ReleasePhaseComplete:
		condStatus = metav1.ConditionTrue
		condReason = "Complete"
		condMsg = fmt.Sprintf("all %d environments synced successfully", synced)
	case kaprov1alpha1.ReleasePhaseFailed:
		condStatus = metav1.ConditionFalse
		condReason = "Failed"
		condMsg = fmt.Sprintf("%d/%d environments synced before failure", synced, totalEnvs)
	default:
		condStatus = metav1.ConditionFalse
		condReason = "Progressing"
		condMsg = fmt.Sprintf("%d/%d environments synced", synced, totalEnvs)
	}
	apimeta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		Reason:             condReason,
		Message:            condMsg,
		LastTransitionTime: metav1.Now(),
	})

	return st
}

// deleteReport removes a ReleaseReport if it still exists.
func (r *ReleaseReportReconciler) deleteReport(ctx context.Context, name, namespace string) error {
	report := &kaprov1alpha1.ReleaseReport{}
	err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, report)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get ReleaseReport for deletion: %w", err)
	}
	return client.IgnoreNotFound(r.Delete(ctx, report))
}

func (r *ReleaseReportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ReleaseReport reconciles on Release changes and on Sync changes
	// that affect a Release.
	return ctrl.NewControllerManagedBy(mgr).
		Named("releasereport").
		For(&kaprov1alpha1.Release{}).
		Watches(
			&kaprov1alpha1.Sync{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
				releaseName := obj.GetLabels()["kapro.io/release"]
				if releaseName == "" {
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: client.ObjectKey{Name: releaseName, Namespace: obj.GetNamespace()},
				}}
			}),
		).
		Complete(r)
}
