package controller

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaprometrics "kapro.io/kapro/internal/metrics"
)

const (
	defaultFleetDriftReportSyncInterval = 5 * time.Minute
	defaultFleetDriftReportMaxTargets   = 128
	maxFleetDriftAppVersions            = 64
	maxFleetDriftObjectsPerTarget       = 16
	defaultFleetDriftAppKey             = "default"
)

// FleetDriftReportReconciler computes a read-only drift report from existing
// runtime status. It writes only FleetDriftReport.status.
type FleetDriftReportReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
	Now      func() time.Time
}

// +kubebuilder:rbac:groups=kapro.io,resources=fleetdriftreports,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=fleetdriftreports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=clusters;promotionruns;targets,verbs=get;list;watch

func (r *FleetDriftReportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var report kaprov1alpha2.FleetDriftReport
	if err := r.Get(ctx, req.NamespacedName, &report); err != nil {
		if apierrors.IsNotFound(err) {
			kaprometrics.DeleteFleetDriftReport(req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !report.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	next, err := r.computeStatus(ctx, &report)
	if err != nil {
		now := metav1.NewTime(r.now())
		next = kaprov1alpha2.FleetDriftReportStatus{
			ObservedGeneration: report.Generation,
			Phase:              kaprov1alpha2.FleetDriftReportPhaseFailed,
			ObservedAt:         &now,
		}
		setFleetDriftReportConditions(&next, report.Generation, "ObservationFailed", err.Error())
	}

	if !reflect.DeepEqual(report.Status, next) {
		before := report.DeepCopy()
		report.Status = next
		if patchErr := r.Status().Patch(ctx, &report, client.MergeFrom(before)); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch FleetDriftReport status: %w", patchErr)
		}
	}
	kaprometrics.ObserveFleetDriftReport(report.Name, next)

	return ctrl.Result{RequeueAfter: fleetDriftReportSyncInterval(&report)}, nil
}

func (r *FleetDriftReportReconciler) computeStatus(ctx context.Context, report *kaprov1alpha2.FleetDriftReport) (kaprov1alpha2.FleetDriftReportStatus, error) {
	selector := labels.Everything()
	if report.Spec.TargetSelector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(report.Spec.TargetSelector)
		if err != nil {
			return kaprov1alpha2.FleetDriftReportStatus{}, fmt.Errorf("parse target selector: %w", err)
		}
	}

	listOptions := []client.ListOption{}
	if report.Spec.TargetSelector != nil {
		listOptions = append(listOptions, client.MatchingLabelsSelector{Selector: selector})
	}
	var targets kaprov1alpha2.TargetList
	if err := r.List(ctx, &targets, listOptions...); err != nil {
		return kaprov1alpha2.FleetDriftReportStatus{}, fmt.Errorf("list Targets: %w", err)
	}
	sort.Slice(targets.Items, func(i, j int) bool {
		return targets.Items[i].Name < targets.Items[j].Name
	})

	clusters, err := r.clusterMap(ctx)
	if err != nil {
		return kaprov1alpha2.FleetDriftReportStatus{}, err
	}
	runFleet, err := r.promotionRunFleetMap(ctx, report.Spec.FleetRef != "")
	if err != nil {
		return kaprov1alpha2.FleetDriftReportStatus{}, err
	}

	maxTargets := fleetDriftReportMaxTargets(report)
	status := kaprov1alpha2.FleetDriftReportStatus{
		ObservedGeneration: report.Generation,
		ObservedAt:         ptrTime(metav1.NewTime(r.now())),
	}

	for i := range targets.Items {
		target := &targets.Items[i]
		if !r.reportIncludesTarget(report, target, selector, runFleet) {
			continue
		}
		obs := observeFleetDriftTarget(target, clusters)
		status.Summary.TotalTargets++
		status.Summary.TotalBackendObjects += obs.totalObjects
		status.Summary.DriftedBackendObjects += obs.driftedObjects
		switch obs.phase {
		case kaprov1alpha2.FleetDriftReportPhaseCurrent:
			status.Summary.CurrentTargets++
		case kaprov1alpha2.FleetDriftReportPhaseDrifted:
			status.Summary.DriftedTargets++
		case kaprov1alpha2.FleetDriftReportPhaseFailed:
			status.Summary.FailedTargets++
		case kaprov1alpha2.FleetDriftReportPhaseUnknown:
			status.Summary.UnknownTargets++
		default:
			status.Summary.PendingTargets++
		}
		if obs.phase != kaprov1alpha2.FleetDriftReportPhaseCurrent && int32(len(status.Targets)) < maxTargets {
			status.Targets = append(status.Targets, obs.target)
		}
	}

	status.Phase = fleetDriftReportPhase(status.Summary)
	reason, message := fleetDriftReportReason(status)
	setFleetDriftReportConditions(&status, report.Generation, reason, message)
	return status, nil
}

func (r *FleetDriftReportReconciler) clusterMap(ctx context.Context) (map[string]kaprov1alpha2.Cluster, error) {
	var list kaprov1alpha2.ClusterList
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list Clusters: %w", err)
	}
	out := make(map[string]kaprov1alpha2.Cluster, len(list.Items))
	for _, cluster := range list.Items {
		out[cluster.Name] = cluster
	}
	return out, nil
}

func (r *FleetDriftReportReconciler) promotionRunFleetMap(ctx context.Context, needed bool) (map[string]string, error) {
	if !needed {
		return nil, nil
	}
	var list kaprov1alpha2.PromotionRunList
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list PromotionRuns: %w", err)
	}
	out := make(map[string]string, len(list.Items))
	for _, run := range list.Items {
		if fleet := run.Labels[promotionFleetLabel]; fleet != "" {
			out[run.Name] = fleet
		}
	}
	return out, nil
}

func (r *FleetDriftReportReconciler) reportIncludesTarget(report *kaprov1alpha2.FleetDriftReport, target *kaprov1alpha2.Target, selector labels.Selector, runFleet map[string]string) bool {
	if report.Spec.PromotionRunRef != "" && target.Spec.PromotionRunRef != report.Spec.PromotionRunRef {
		return false
	}
	if report.Spec.FleetRef != "" && runFleet[target.Spec.PromotionRunRef] != report.Spec.FleetRef {
		return false
	}
	return selector.Matches(labels.Set(target.Labels))
}

func (r *FleetDriftReportReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

type fleetDriftTargetObservation struct {
	phase          kaprov1alpha2.FleetDriftReportPhase
	target         kaprov1alpha2.FleetDriftTarget
	totalObjects   int32
	driftedObjects int32
}

func observeFleetDriftTarget(target *kaprov1alpha2.Target, clusters map[string]kaprov1alpha2.Cluster) fleetDriftTargetObservation {
	out := fleetDriftTargetObservation{
		phase: kaprov1alpha2.FleetDriftReportPhasePending,
		target: kaprov1alpha2.FleetDriftTarget{
			PromotionRun: target.Spec.PromotionRunRef,
			PlanRef:      target.Spec.PlanRef,
			Plan:         target.Spec.Plan,
			Stage:        target.Spec.Stage,
			Cluster:      target.Spec.Target,
			Phase:        target.Status.Phase,
		},
	}

	cluster, found := clusters[target.Spec.Target]
	if !found {
		out.phase = kaprov1alpha2.FleetDriftReportPhaseUnknown
		out.target.Reason = "ClusterMissing"
		out.target.Message = fmt.Sprintf("cluster %q was not found", target.Spec.Target)
		out.target.AppVersions = desiredVersionsForTarget(target)
		out.target.Objects, out.totalObjects, out.driftedObjects = driftObjectsForTarget(target)
		return out
	}

	versionObs := observeVersionsForTarget(target, cluster)
	out.target.AppVersions = versionObs.versions
	out.target.Objects, out.totalObjects, out.driftedObjects = driftObjectsForTarget(target)

	if target.Status.Phase == kaprov1alpha2.TargetPhaseFailed {
		out.phase = kaprov1alpha2.FleetDriftReportPhaseFailed
		out.target.Reason = "TargetFailed"
		out.target.Message = nonEmpty(target.Status.Message, "target failed")
		return out
	}
	if versionObs.deliveryFailed {
		out.phase = kaprov1alpha2.FleetDriftReportPhaseFailed
		out.target.Reason = "DeliveryFailed"
		out.target.Message = "cluster delivery status reports failure"
		return out
	}
	if versionObs.drifted || out.driftedObjects > 0 {
		if target.Status.Phase == kaprov1alpha2.TargetPhaseConverged {
			out.phase = kaprov1alpha2.FleetDriftReportPhaseDrifted
			out.target.Reason, out.target.Message = targetDriftReasonAndMessage(versionObs.drifted, out.driftedObjects > 0)
			return out
		}
		out.phase = kaprov1alpha2.FleetDriftReportPhasePending
		out.target.Reason = "RolloutPending"
		out.target.Message = "target has not converged to desired version yet"
		return out
	}
	if versionObs.unknown {
		out.phase = kaprov1alpha2.FleetDriftReportPhaseUnknown
		out.target.Reason = "SignalsIncomplete"
		out.target.Message = "cluster has not reported current version for every desired app"
		return out
	}
	if target.Status.Phase != "" && target.Status.Phase != kaprov1alpha2.TargetPhaseConverged {
		out.phase = kaprov1alpha2.FleetDriftReportPhasePending
		out.target.Reason = "RolloutPending"
		out.target.Message = "target is still progressing"
		return out
	}

	out.phase = kaprov1alpha2.FleetDriftReportPhaseCurrent
	out.target.Reason = "Current"
	out.target.Message = "desired and observed versions match"
	return out
}

func desiredVersionsForTarget(target *kaprov1alpha2.Target) []kaprov1alpha2.FleetDriftVersion {
	desired := targetDesiredMap(target)
	keys := cappedSortedKeys(desired, maxFleetDriftAppVersions)
	out := make([]kaprov1alpha2.FleetDriftVersion, 0, len(keys))
	for _, key := range keys {
		out = append(out, kaprov1alpha2.FleetDriftVersion{AppKey: key, DesiredVersion: desired[key]})
	}
	return out
}

type fleetDriftVersionObservation struct {
	versions       []kaprov1alpha2.FleetDriftVersion
	drifted        bool
	unknown        bool
	deliveryFailed bool
}

func observeVersionsForTarget(target *kaprov1alpha2.Target, cluster kaprov1alpha2.Cluster) fleetDriftVersionObservation {
	desired := targetDesiredMap(target)
	keys := sortedKeys(desired)
	out := fleetDriftVersionObservation{
		versions: make([]kaprov1alpha2.FleetDriftVersion, 0, min(len(keys), maxFleetDriftAppVersions)),
		unknown:  len(keys) == 0,
	}
	for i, key := range keys {
		current := cluster.Status.CurrentVersions[key]
		if current == "" && key == defaultFleetDriftAppKey {
			current = cluster.Status.Version
		}
		version := kaprov1alpha2.FleetDriftVersion{
			AppKey:         key,
			DesiredVersion: desired[key],
			CurrentVersion: current,
		}
		if delivery, ok := cluster.Status.Delivery[key]; ok {
			version.DeliveryPhase = delivery.Phase
		}
		if i < maxFleetDriftAppVersions {
			out.versions = append(out.versions, version)
		}
		if version.DeliveryPhase == kaprov1alpha2.DeliveryPhaseFailed {
			out.deliveryFailed = true
		}
		if version.DesiredVersion != "" && version.CurrentVersion == "" {
			out.unknown = true
		}
		if version.DesiredVersion != "" && version.CurrentVersion != "" && version.DesiredVersion != version.CurrentVersion {
			out.drifted = true
		}
	}
	return out
}

func targetDriftReasonAndMessage(versionDrift, objectDrift bool) (string, string) {
	switch {
	case versionDrift && objectDrift:
		return "VersionAndBackendObjectDrift", "observed app version and backend object evidence differ from desired state"
	case objectDrift:
		return "BackendObjectDrift", "backend object evidence differs from desired state"
	default:
		return "VersionDrift", "observed app version differs from desired version"
	}
}

func targetDesiredMap(target *kaprov1alpha2.Target) map[string]string {
	desired := copyStringMap(target.Spec.DesiredVersions)
	if len(desired) == 0 {
		desired = copyStringMap(target.Status.DesiredVersions)
	}
	if len(desired) == 0 && target.Spec.Version != "" {
		key := target.Spec.AppKey
		if key == "" {
			key = defaultFleetDriftAppKey
		}
		desired = map[string]string{key: target.Spec.Version}
	}
	return desired
}

func driftObjectsForTarget(target *kaprov1alpha2.Target) ([]kaprov1alpha2.FleetDriftObject, int32, int32) {
	total := int32(len(target.Status.BackendObjects))
	drifted := int32(0)
	objects := make([]kaprov1alpha2.FleetDriftObject, 0, min(len(target.Status.BackendObjects), maxFleetDriftObjectsPerTarget))
	for _, object := range target.Status.BackendObjects {
		isDrifted := object.DesiredVersion != "" && object.CurrentVersion != "" && object.DesiredVersion != object.CurrentVersion
		if isDrifted {
			drifted++
		}
		if (isDrifted || object.Phase == "Failed") && len(objects) < maxFleetDriftObjectsPerTarget {
			objects = append(objects, kaprov1alpha2.FleetDriftObject(object))
		}
	}
	return objects, total, drifted
}

func fleetDriftReportPhase(summary kaprov1alpha2.FleetDriftSummary) kaprov1alpha2.FleetDriftReportPhase {
	switch {
	case summary.FailedTargets > 0:
		return kaprov1alpha2.FleetDriftReportPhaseFailed
	case summary.DriftedTargets > 0 || summary.DriftedBackendObjects > 0:
		return kaprov1alpha2.FleetDriftReportPhaseDrifted
	case summary.UnknownTargets > 0:
		return kaprov1alpha2.FleetDriftReportPhaseUnknown
	case summary.PendingTargets > 0:
		return kaprov1alpha2.FleetDriftReportPhasePending
	default:
		return kaprov1alpha2.FleetDriftReportPhaseCurrent
	}
}

func fleetDriftReportReason(status kaprov1alpha2.FleetDriftReportStatus) (string, string) {
	switch status.Phase {
	case kaprov1alpha2.FleetDriftReportPhaseCurrent:
		return "Current", "all observed targets match desired versions"
	case kaprov1alpha2.FleetDriftReportPhaseDrifted:
		return "DriftDetected", "one or more converged targets differ from desired versions"
	case kaprov1alpha2.FleetDriftReportPhaseFailed:
		return "TargetFailed", "one or more targets or delivery loops report failure"
	case kaprov1alpha2.FleetDriftReportPhaseUnknown:
		return "SignalsIncomplete", "one or more targets are missing cluster or version signals"
	default:
		return "RolloutPending", "one or more targets are still converging"
	}
}

func setFleetDriftReportConditions(status *kaprov1alpha2.FleetDriftReportStatus, generation int64, reason, message string) {
	readyStatus := metav1.ConditionFalse
	reconcilingStatus := metav1.ConditionFalse
	stalledStatus := metav1.ConditionFalse
	if status.Phase == kaprov1alpha2.FleetDriftReportPhaseCurrent {
		readyStatus = metav1.ConditionTrue
	}
	if status.Phase == kaprov1alpha2.FleetDriftReportPhasePending {
		reconcilingStatus = metav1.ConditionTrue
	}
	if status.Phase == kaprov1alpha2.FleetDriftReportPhaseDrifted ||
		status.Phase == kaprov1alpha2.FleetDriftReportPhaseFailed ||
		status.Phase == kaprov1alpha2.FleetDriftReportPhaseUnknown {
		stalledStatus = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeReady,
		Status:             readyStatus,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeReconciling,
		Status:             reconcilingStatus,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeStalled,
		Status:             stalledStatus,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

func fleetDriftReportMaxTargets(report *kaprov1alpha2.FleetDriftReport) int32 {
	if report.Spec.MaxTargets != nil && *report.Spec.MaxTargets > 0 {
		return *report.Spec.MaxTargets
	}
	return defaultFleetDriftReportMaxTargets
}

func fleetDriftReportSyncInterval(report *kaprov1alpha2.FleetDriftReport) time.Duration {
	if report.Spec.SyncInterval != nil && report.Spec.SyncInterval.Duration > 0 {
		return report.Spec.SyncInterval.Duration
	}
	return defaultFleetDriftReportSyncInterval
}

func ptrTime(t metav1.Time) *metav1.Time {
	return &t
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cappedSortedKeys(values map[string]string, max int) []string {
	keys := sortedKeys(values)
	if len(keys) > max {
		return keys[:max]
	}
	return keys
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (r *FleetDriftReportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.FleetDriftReport{}).
		Watches(&kaprov1alpha2.Target{}, handler.EnqueueRequestsFromMapFunc(r.allReportRequests)).
		Watches(&kaprov1alpha2.Cluster{}, handler.EnqueueRequestsFromMapFunc(r.allReportRequests)).
		Watches(&kaprov1alpha2.PromotionRun{}, handler.EnqueueRequestsFromMapFunc(r.allReportRequests)).
		Complete(r)
}

func (r *FleetDriftReportReconciler) allReportRequests(ctx context.Context, _ client.Object) []reconcile.Request {
	var reports kaprov1alpha2.FleetDriftReportList
	if err := r.List(ctx, &reports); err != nil {
		if !apierrors.IsNotFound(err) {
			ctrl.Log.WithName("fleetdriftreport").Error(err, "list FleetDriftReports for watch fanout")
		}
		return nil
	}
	requests := make([]reconcile.Request, 0, len(reports.Items))
	for _, report := range reports.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: report.Name}})
	}
	return requests
}
