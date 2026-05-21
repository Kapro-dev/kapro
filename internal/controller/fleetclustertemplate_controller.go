package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/provider"
)

const (
	defaultFleetTemplateInterval = 5 * time.Minute
)

// DiscovererFactory returns a Discoverer for a given template source. Pluggable
// so tests can inject fakes without hitting live cloud APIs.
type DiscovererFactory func(kaprov1alpha2.ClusterTemplateSource) (provider.Discoverer, error)

// FleetClusterTemplateReconciler is the universal fleet auto-import reconciler.
//
// It is discoverer-agnostic: every cloud or platform is one Discoverer
// implementation behind the same interface. The public preview ships the GCP
// Fleet discoverer; AWS / Azure / RHACM / CAPI / static are preview stubs and
// surface a Stalled condition until their discoverers land.
//
// Ownership model:
//   - Imported FleetClusters carry an ownerReference + the
//     kapro.io/managed-by=fleetclustertemplate label.
//   - The reconciler upserts only objects with that label. Hand-authored
//     FleetClusters that happen to share a name with a discovered cluster are
//     left untouched and surfaced via a warning event.
//   - spec is written once at create time. Subsequent reconciles only
//     refresh labels/annotations — changing spec on an imported FleetCluster
//     (e.g. spec.desiredVersions written by the PromotionRun controller) is
//     preserved. Operators who need to roll the template re-create the
//     imported FleetClusters by deleting them; ownerRef GC plus the next
//     poll re-applies the new template.
type FleetClusterTemplateReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// DiscovererFactory is provider.NewDiscoverer in production. Tests
	// override to avoid live cloud calls.
	DiscovererFactory DiscovererFactory
}

// +kubebuilder:rbac:groups=kapro.io,resources=clustertemplates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=clustertemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=clustertemplates/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete

func (r *FleetClusterTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("clustertemplate", req.Name)

	var tmpl kaprov1alpha2.ClusterTemplate
	if err := r.Get(ctx, req.NamespacedName, &tmpl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Capture the base for a strategic-merge status patch at the end.
	// Mutate tmpl.Status freely throughout reconcile — only the diff against
	// statusBase is shipped to the apiserver. Matches BackendProfileReconciler.
	statusBase := tmpl.DeepCopy()

	requeue := r.intervalOrDefault(&tmpl)

	// SourceKind is set from spec FIRST so it shows up in printcolumns even
	// when the template is suspended or its source branch is unimplemented.
	tmpl.Status.SourceKind = sourceKindFromSpec(tmpl.Spec.Source)

	if tmpl.Spec.Suspend {
		// Suspended is intentional pause — Ready stays False but Stalled
		// is not set (it would falsely alert that the controller is stuck).
		apimeta.SetStatusCondition(&tmpl.Status.Conditions, metav1.Condition{
			Type:               kaprov1alpha2.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "Suspended",
			Message:            "reconciliation suspended by spec.suspend",
			ObservedGeneration: tmpl.Generation,
		})
		apimeta.RemoveStatusCondition(&tmpl.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
		if err := r.patchStatus(ctx, statusBase, &tmpl); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	disco, err := r.factory()(tmpl.Spec.Source)
	if err != nil {
		if provider.IsSourceNotImplemented(err) {
			r.setReady(&tmpl, metav1.ConditionFalse, "SourceNotImplemented", err.Error())
			r.eventf(&tmpl, "Warning", "SourceNotImplemented", "%v", err)
			// Don't requeue tight; operator must edit spec.source.
			return ctrl.Result{}, r.patchStatus(ctx, statusBase, &tmpl)
		}
		r.setReady(&tmpl, metav1.ConditionFalse, "InvalidSource", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, statusBase, &tmpl)
	}
	tmpl.Status.SourceKind = disco.SourceKind()

	discovered, err := disco.List(ctx)
	if err != nil {
		r.setReady(&tmpl, metav1.ConditionFalse, "DiscoveryError", err.Error())
		r.eventf(&tmpl, "Warning", "DiscoveryError", "%v", err)
		if perr := r.patchStatus(ctx, statusBase, &tmpl); perr != nil {
			logger.Error(perr, "patch status after DiscoveryError")
		}
		return ctrl.Result{RequeueAfter: requeue}, fmt.Errorf("discover clusters: %w", err)
	}
	tmpl.Status.DiscoveredClusters = int32(len(discovered))

	sel, err := labelSelectorOrAll(tmpl.Spec.Selector)
	if err != nil {
		r.setReady(&tmpl, metav1.ConditionFalse, "InvalidSelector", err.Error())
		return ctrl.Result{RequeueAfter: requeue}, r.patchStatus(ctx, statusBase, &tmpl)
	}

	providerSpec := disco.Provider()
	kept := make(map[string]struct{}, len(discovered))
	var upsertErrs []string
	var imported int32

	for _, c := range discovered {
		if !sel.Matches(labels.Set(c.Labels)) {
			continue
		}
		name := sanitiseClusterName(c.Name)
		if name == "" {
			r.eventf(&tmpl, "Warning", "InvalidClusterName",
				"discovered cluster %q is not a valid Kubernetes object name; skipping", c.Name)
			continue
		}
		kept[name] = struct{}{}
		if err := r.upsertFleetCluster(ctx, &tmpl, c, providerSpec, name); err != nil {
			logger.Error(err, "upsert FleetCluster", "name", name)
			upsertErrs = append(upsertErrs, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		imported++
	}
	tmpl.Status.ImportedClusters = imported

	if tmpl.Spec.Prune {
		if err := r.pruneOrphans(ctx, &tmpl, kept); err != nil {
			logger.Error(err, "prune orphans")
			upsertErrs = append(upsertErrs, fmt.Sprintf("prune: %v", err))
		}
	}

	now := metav1.Now()
	tmpl.Status.LastSyncTime = &now

	if len(upsertErrs) > 0 {
		r.setReady(&tmpl, metav1.ConditionFalse, "UpsertErrors",
			fmt.Sprintf("%d/%d clusters failed: %s",
				len(upsertErrs), len(discovered), upsertErrs[0]))
	} else {
		r.setReady(&tmpl, metav1.ConditionTrue, "Synced",
			fmt.Sprintf("discovered %d, imported %d (source=%s)",
				len(discovered), imported, disco.SourceKind()))
	}

	if err := r.patchStatus(ctx, statusBase, &tmpl); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *FleetClusterTemplateReconciler) factory() DiscovererFactory {
	if r.DiscovererFactory != nil {
		return r.DiscovererFactory
	}
	return provider.NewDiscoverer
}

func (r *FleetClusterTemplateReconciler) intervalOrDefault(tmpl *kaprov1alpha2.ClusterTemplate) time.Duration {
	if tmpl.Spec.Interval == "" {
		return defaultFleetTemplateInterval
	}
	d, err := time.ParseDuration(tmpl.Spec.Interval)
	if err != nil || d <= 0 {
		return defaultFleetTemplateInterval
	}
	return d
}

// sourceKindFromSpec returns the short identifier for whichever source branch
// is set on the template. Used to populate status.sourceKind even when the
// discoverer for that branch isn't implemented yet (otherwise the printcolumn
// would render empty for in-flight stubs).
func sourceKindFromSpec(src kaprov1alpha2.ClusterTemplateSource) string {
	switch {
	case src.GCP != nil:
		return "gcp"
	case src.AWS != nil:
		return "aws"
	case src.Azure != nil:
		return "azure"
	case src.RHACM != nil:
		return "rhacm"
	case src.CAPI != nil:
		return "capi"
	case src.Static != nil:
		return "static"
	default:
		return ""
	}
}

func labelSelectorOrAll(sel *metav1.LabelSelector) (labels.Selector, error) {
	if sel == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(sel)
}

// upsertFleetCluster creates a managed FleetCluster from the template + a
// discovered cluster, or refreshes only the labels/annotations of an existing
// managed object. Hand-authored FleetClusters (no managed-by label) are never
// touched.
func (r *FleetClusterTemplateReconciler) upsertFleetCluster(
	ctx context.Context,
	tmpl *kaprov1alpha2.ClusterTemplate,
	c provider.ClusterInfo,
	providerSpec kaprov1alpha2.ClusterProvider,
	name string,
) error {
	var fc kaprov1alpha2.Cluster
	err := r.Get(ctx, client.ObjectKey{Name: name}, &fc)
	switch {
	case apierrors.IsNotFound(err):
		fc = kaprov1alpha2.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		r.applyManagedMetadata(tmpl, &fc, c)
		// spec is written verbatim from the template + derived provider.
		fc.Spec = *tmpl.Spec.Template.Spec.DeepCopy()
		fc.Spec.Provider = providerSpec.DeepCopy()
		if err := controllerutil.SetControllerReference(tmpl, &fc, r.Scheme); err != nil {
			return fmt.Errorf("set ownerReference: %w", err)
		}
		if err := r.Create(ctx, &fc); err != nil {
			return fmt.Errorf("create FleetCluster: %w", err)
		}
		r.eventf(tmpl, "Normal", "FleetClusterImported",
			"imported %s cluster %q as FleetCluster %q",
			providerSpec.Kind, c.Name, name)
		return nil
	case err != nil:
		return fmt.Errorf("get FleetCluster: %w", err)
	}

	if fc.Labels[kaprov1alpha2.ClusterTemplateManagedByLabel] != kaprov1alpha2.ClusterTemplateManagedByValue {
		r.eventf(tmpl, "Warning", "FleetClusterUnmanaged",
			"FleetCluster %q exists but is not managed by a FleetClusterTemplate — skipping", name)
		return nil
	}

	// Existing managed object: refresh labels/annotations only. spec is left
	// alone so PromotionRun-written fields (desiredVersions, etc.) are
	// preserved. Template changes propagate by recreating the FleetCluster
	// (delete → ownerRef GC → next poll re-imports).
	patch := client.MergeFrom(fc.DeepCopy())
	r.applyManagedMetadata(tmpl, &fc, c)
	if err := r.Patch(ctx, &fc, patch); err != nil {
		return fmt.Errorf("patch FleetCluster: %w", err)
	}
	return nil
}

// applyManagedMetadata stamps the managed-by labels, template-supplied
// labels/annotations, and source-reported labels onto the FleetCluster.
// Order (later wins on conflict):
//
//  1. Source-reported labels (from cluster discovery).
//  2. Template's metadata.labels.
//  3. The managed-by markers (always win).
func (r *FleetClusterTemplateReconciler) applyManagedMetadata(
	tmpl *kaprov1alpha2.ClusterTemplate,
	fc *kaprov1alpha2.Cluster,
	c provider.ClusterInfo,
) {
	if fc.Labels == nil {
		fc.Labels = map[string]string{}
	}
	for k, v := range c.Labels {
		fc.Labels[k] = v
	}
	for k, v := range tmpl.Spec.Template.Metadata.Labels {
		fc.Labels[k] = v
	}
	fc.Labels[kaprov1alpha2.ClusterTemplateManagedByLabel] = kaprov1alpha2.ClusterTemplateManagedByValue
	fc.Labels[kaprov1alpha2.ClusterTemplateNameLabel] = tmpl.Name

	if len(tmpl.Spec.Template.Metadata.Annotations) > 0 {
		if fc.Annotations == nil {
			fc.Annotations = map[string]string{}
		}
		for k, v := range tmpl.Spec.Template.Metadata.Annotations {
			fc.Annotations[k] = v
		}
	}
}

// pruneOrphans deletes managed FleetClusters whose discovered counterpart
// has disappeared from the source. Only fires when spec.prune=true.
func (r *FleetClusterTemplateReconciler) pruneOrphans(
	ctx context.Context,
	tmpl *kaprov1alpha2.ClusterTemplate,
	kept map[string]struct{},
) error {
	var owned kaprov1alpha2.ClusterList
	if err := r.List(ctx, &owned, client.MatchingLabels{
		kaprov1alpha2.ClusterTemplateManagedByLabel: kaprov1alpha2.ClusterTemplateManagedByValue,
		kaprov1alpha2.ClusterTemplateNameLabel:      tmpl.Name,
	}); err != nil {
		return fmt.Errorf("list owned FleetClusters: %w", err)
	}
	for i := range owned.Items {
		fc := &owned.Items[i]
		if _, alive := kept[fc.Name]; alive {
			continue
		}
		if err := r.Delete(ctx, fc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete orphan %s: %w", fc.Name, err)
		}
		r.eventf(tmpl, "Normal", "FleetClusterPruned",
			"deleted orphan FleetCluster %q — source no longer reports it", fc.Name)
	}
	return nil
}

// sanitiseClusterName returns the discovered cluster name unchanged if it is
// a valid DNS-1123 subdomain (the Kubernetes object-name rule), otherwise
// empty. Uses the apimachinery helper so we catch the same cases the
// apiserver would reject — leading/trailing '-' or '.', invalid characters,
// length > 253. Cloud sources typically already enforce this, but a
// hand-typed StaticFleetSource entry might not.
func sanitiseClusterName(name string) string {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return ""
	}
	return name
}

// setReady writes the Ready condition and the paired Stalled condition.
// Pattern matches BackendProfileReconciler: when Ready=False the controller
// is also Stalled (cannot progress without operator action — invalid source,
// not-yet-implemented branch, label-selector parse error, etc.). When
// Ready=True the Stalled condition is removed.
func (r *FleetClusterTemplateReconciler) setReady(tmpl *kaprov1alpha2.ClusterTemplate, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&tmpl.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: tmpl.Generation,
	})
	if status == metav1.ConditionTrue {
		apimeta.RemoveStatusCondition(&tmpl.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
		return
	}
	apimeta.SetStatusCondition(&tmpl.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeStalled,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: tmpl.Generation,
	})
}

// patchStatus ships a strategic-merge status patch derived from the
// base+mutated pair. Using MergeFrom+Patch (rather than Status().Update) is
// the convention in the codebase (see BackendProfileReconciler) — it's
// less conflict-prone and only the diff is transmitted.
func (r *FleetClusterTemplateReconciler) patchStatus(ctx context.Context, base, tmpl *kaprov1alpha2.ClusterTemplate) error {
	tmpl.Status.ObservedGeneration = tmpl.Generation
	if err := r.Status().Patch(ctx, tmpl, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch FleetClusterTemplate status: %w", err)
	}
	return nil
}

func (r *FleetClusterTemplateReconciler) eventf(tmpl *kaprov1alpha2.ClusterTemplate, eventType, reason, format string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(tmpl, eventType, reason, format, args...)
}

func (r *FleetClusterTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.ClusterTemplate{}).
		Owns(&kaprov1alpha2.Cluster{}).
		Complete(r)
}
