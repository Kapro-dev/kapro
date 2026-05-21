package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// argoClusterSecretLabel is the well-known Argo CD label that identifies a
// cluster Secret in the argocd namespace. Used to short-circuit Secret watch
// events at predicate time so unrelated Secret churn doesn't enqueue every
// BackendProfile reconcile.
const argoClusterSecretLabel = "argocd.argoproj.io/secret-type"
const argoClusterSecretValue = "cluster"

// argoClusterSecretPredicate accepts only Secret events whose label matches
// argocd.argoproj.io/secret-type=cluster. Applied to the Secret watch so the
// generic mapping function never runs for unrelated Secrets.
var argoClusterSecretPredicate = predicate.NewPredicateFuncs(func(obj client.Object) bool {
	if obj == nil {
		return false
	}
	return obj.GetLabels()[argoClusterSecretLabel] == argoClusterSecretValue
})

// Compile-time guard so event package import stays referenced if a future edit
// re-uses event filtering. Cheap, removes "unused import" diagnostics noise.
var _ = event.CreateEvent{}

const backendProfileDiscoveryRequeue = 2 * time.Minute

// BackendProfileReconciler records readiness for selectable delivery backends.
type BackendProfileReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=backendprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=backendprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=pluginregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch
// +kubebuilder:rbac:groups=argoproj.io,resources=applicationsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories;ocirepositories;buckets,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch

const maxBackendDiscoveryStatusObjects = 128
const defaultBackendDiscoveryMaxObjects int64 = 1000

func (r *BackendProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var profile kaprov1alpha2.Backend
	if err := r.Get(ctx, req.NamespacedName, &profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	patch := client.MergeFrom(profile.DeepCopy())
	now := metav1.Now()
	ready, reason, message := r.profileReadiness(ctx, &profile)

	profile.Status.ObservedGeneration = profile.Generation
	profile.Status.Ready = ready
	profile.Status.Driver = profile.Spec.Driver
	profile.Status.Runtime = profile.Spec.Runtime
	if profile.Status.Runtime == "" {
		profile.Status.Runtime = kaprov1alpha2.BackendRuntimeBoth
	}
	discovery, discoveryReason, discoveryMessage := r.observeDiscovery(ctx, &profile)
	profile.Status.DiscoveredClusters = discovery.clusters
	profile.Status.DiscoveredApplications = discovery.applications
	profile.Status.DiscoveredApplicationSets = discovery.applicationSets
	profile.Status.SelectedObjects = discovery.selected
	profile.Status.SkippedObjects = discovery.skipped
	profile.Status.UnsupportedPatterns = discovery.unsupported
	profile.Status.DiscoveryErrors = discovery.errors
	if profile.Spec.Discovery != nil && profile.Spec.Discovery.Enabled {
		profile.Status.LastDiscoveryTime = &now
	} else {
		profile.Status.LastDiscoveryTime = nil
	}

	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: profile.Generation,
		LastTransitionTime: now,
	})
	if profile.Spec.Discovery != nil && profile.Spec.Discovery.Enabled {
		apimeta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{
			Type:               "DiscoveryReady",
			Status:             discovery.status,
			Reason:             discoveryReason,
			Message:            discoveryMessage,
			ObservedGeneration: profile.Generation,
			LastTransitionTime: now,
		})
	} else {
		apimeta.RemoveStatusCondition(&profile.Status.Conditions, "DiscoveryReady")
	}
	if ready {
		apimeta.RemoveStatusCondition(&profile.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	} else {
		apimeta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{
			Type:               kaprov1alpha2.ConditionTypeStalled,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: profile.Generation,
			LastTransitionTime: now,
		})
	}

	if err := r.Status().Patch(ctx, &profile, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch BackendProfile status: %w", err)
	}
	if profile.Spec.Discovery != nil && profile.Spec.Discovery.Enabled {
		return ctrl.Result{RequeueAfter: backendProfileDiscoveryRequeue}, nil
	}
	return ctrl.Result{}, nil
}

type backendDiscoveryCounts struct {
	clusters        int32
	applications    int32
	applicationSets int32
	status          metav1.ConditionStatus
	selected        []kaprov1alpha2.DiscoveredBackendObject
	skipped         []kaprov1alpha2.DiscoveredBackendObject
	unsupported     []kaprov1alpha2.DiscoveredBackendObject
	errors          []string
}

func (r *BackendProfileReconciler) observeDiscovery(ctx context.Context, profile *kaprov1alpha2.Backend) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	if profile.Spec.Discovery == nil || !profile.Spec.Discovery.Enabled {
		return counts, "DiscoveryDisabled", "backend discovery is disabled"
	}
	namespace := "argocd"
	if profile.Spec.Driver == kaprov1alpha2.BackendDriverFlux {
		namespace = "flux-system"
	}
	if profile.Spec.Parameters["namespace"] != "" {
		namespace = profile.Spec.Parameters["namespace"]
	}

	switch profile.Spec.Driver {
	case kaprov1alpha2.BackendDriverArgo:
		return r.observeArgoDiscovery(ctx, profile, namespace)
	case kaprov1alpha2.BackendDriverFlux:
		return r.observeFluxDiscovery(ctx, profile, namespace)
	default:
		return counts, "DiscoveryUnsupported", fmt.Sprintf("discovery is not implemented for %s backends", profile.Spec.Driver)
	}
}

func (r *BackendProfileReconciler) observeArgoDiscovery(ctx context.Context, profile *kaprov1alpha2.Backend, namespace string) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	selector := labels.Everything()
	if profile.Spec.Discovery.Selector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(profile.Spec.Discovery.Selector)
		if err != nil {
			return backendDiscoveryCounts{status: metav1.ConditionFalse}, "InvalidSelector", err.Error()
		}
	}
	limit := backendDiscoveryListLimit(profile)
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList,
		client.InNamespace(namespace),
		client.MatchingLabels{"argocd.argoproj.io/secret-type": "cluster"},
		client.Limit(limit+1),
	); err != nil {
		return backendDiscoveryCounts{status: metav1.ConditionFalse}, "ClusterDiscoveryFailed", err.Error()
	}
	if exceededListLimit(secretList.Continue, len(secretList.Items), limit) {
		return backendDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("Argo CD cluster Secret discovery exceeded maxObjects=%d; narrow spec.discovery.selector", limit)
	}
	for _, secret := range secretList.Items {
		if selector.Matches(labels.Set(secret.Labels)) {
			counts.clusters++
			counts.addSelected(kaprov1alpha2.DiscoveredBackendObject{
				APIVersion: "v1",
				Kind:       "Secret",
				Namespace:  secret.Namespace,
				Name:       secret.Name,
				Pattern:    "argocd-cluster-secret",
				Reason:     "selected Argo CD cluster Secret",
			})
		}
	}

	appList := &unstructured.UnstructuredList{}
	appList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList",
	})
	if err := r.List(ctx, appList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}, client.Limit(limit+1)); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return counts, "ApplicationDiscoveryUnavailable", "Argo CD Application CRD is not installed"
		}
		return counts, "ApplicationDiscoveryFailed", err.Error()
	}
	if exceededListLimit(appList.GetContinue(), len(appList.Items), limit) {
		return backendDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("Argo CD Application discovery exceeded maxObjects=%d; narrow spec.discovery.selector", limit)
	}
	counts.applications = int32(len(appList.Items))
	for i := range appList.Items {
		app := &appList.Items[i]
		pattern := argoApplicationPattern(app)
		entry := kaprov1alpha2.DiscoveredBackendObject{
			APIVersion:   "argoproj.io/v1alpha1",
			Kind:         "Application",
			Namespace:    app.GetNamespace(),
			Name:         app.GetName(),
			Pattern:      pattern,
			Unit:         argoPromotionUnit(app),
			VersionField: "spec.source.targetRevision",
		}
		switch pattern {
		case "app-of-apps-root":
			entry.Reason = "root app-of-apps objects package child Applications; select child Applications for promotion writes"
			counts.addUnsupported(entry)
		case "applicationset-child":
			entry.Reason = "generated ApplicationSet children are reconciled from the ApplicationSet template; use Git-native generator input writes or the ApplicationSet actuator plugin"
			counts.addSkipped(entry)
		default:
			entry.Reason = "selected Argo CD Application promotion target"
			counts.addSelected(entry)
		}
	}

	appSetList := &unstructured.UnstructuredList{}
	appSetList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationSetList",
	})
	if err := r.List(ctx, appSetList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}, client.Limit(limit+1)); err != nil {
		if !apierrors.IsNotFound(err) && !apimeta.IsNoMatchError(err) {
			return counts, "ApplicationSetDiscoveryFailed", err.Error()
		}
	} else {
		if exceededListLimit(appSetList.GetContinue(), len(appSetList.Items), limit) {
			return backendDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
				fmt.Sprintf("Argo CD ApplicationSet discovery exceeded maxObjects=%d; narrow spec.discovery.selector", limit)
		}
		counts.applicationSets = int32(len(appSetList.Items))
		for i := range appSetList.Items {
			appSet := &appSetList.Items[i]
			counts.addSkipped(kaprov1alpha2.DiscoveredBackendObject{
				APIVersion:   "argoproj.io/v1alpha1",
				Kind:         "ApplicationSet",
				Namespace:    appSet.GetNamespace(),
				Name:         appSet.GetName(),
				Pattern:      "applicationset",
				Reason:       "built-in adoption writes generated Applications; use the ApplicationSet actuator plugin to write templates",
				Unit:         argoPromotionUnit(appSet),
				VersionField: "spec.template.spec.source.targetRevision",
			})
		}
	}
	return counts, "DiscoverySucceeded", counts.summary("Argo")
}

func (r *BackendProfileReconciler) observeFluxDiscovery(ctx context.Context, profile *kaprov1alpha2.Backend, namespace string) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	appSelector := labels.Everything()
	if profile.Spec.Discovery.Selector != nil {
		var err error
		appSelector, err = metav1.LabelSelectorAsSelector(profile.Spec.Discovery.Selector)
		if err != nil {
			return counts, "InvalidSelector", err.Error()
		}
	}
	limit := backendDiscoveryListLimit(profile)

	gitRepositories, reason, message := r.listFluxSourceObjects(ctx, namespace, appSelector, schema.GroupVersionKind{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepositoryList",
	}, "GitRepository", "gitrepository", limit)
	if reason != "" {
		return counts, reason, message
	}
	ociRepositories, reason, message := r.listFluxSourceObjects(ctx, namespace, appSelector, schema.GroupVersionKind{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "OCIRepositoryList",
	}, "OCIRepository", "ocirepository", limit)
	if reason != "" {
		return counts, reason, message
	}
	buckets, reason, message := r.listFluxSourceObjects(ctx, namespace, appSelector, schema.GroupVersionKind{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "BucketList",
	}, "Bucket", "bucket", limit)
	if reason != "" {
		return counts, reason, message
	}
	helmReleases, reason, message := r.listFluxObjects(ctx, namespace, appSelector, schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList",
	}, "HelmRelease", "helmrelease", "spec.chart.spec.version", limit)
	if reason != "" {
		return counts, reason, message
	}
	kustomizations, reason, message := r.listFluxObjects(ctx, namespace, appSelector, schema.GroupVersionKind{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList",
	}, "Kustomization", "kustomization", "spec.sourceRef.name + spec.path + source revision", limit)
	if reason != "" {
		return counts, reason, message
	}
	counts.merge(gitRepositories)
	counts.merge(ociRepositories)
	counts.merge(buckets)
	counts.merge(helmReleases)
	counts.merge(kustomizations)
	return counts, "DiscoverySucceeded", counts.summary("Flux")
}

func (r *BackendProfileReconciler) listFluxSourceObjects(ctx context.Context, namespace string, selector labels.Selector, gvk schema.GroupVersionKind, kind, pattern string, limit int64) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}, client.Limit(limit+1)); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return counts, "", ""
		}
		return counts, "SourceDiscoveryFailed", err.Error()
	}
	if exceededListLimit(list.GetContinue(), len(list.Items), limit) {
		return backendDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("%s discovery exceeded maxObjects=%d; narrow spec.discovery.selector", kind, limit)
	}
	counts.applications = int32(len(list.Items))
	for i := range list.Items {
		obj := &list.Items[i]
		counts.addSelected(kaprov1alpha2.DiscoveredBackendObject{
			APIVersion:   gvk.GroupVersion().String(),
			Kind:         kind,
			Namespace:    obj.GetNamespace(),
			Name:         obj.GetName(),
			Pattern:      pattern,
			Reason:       "selected Flux source revision target",
			Unit:         backendObjectUnit(obj),
			VersionField: fluxSourceVersionField(obj),
		})
	}
	return counts, "", ""
}

func (r *BackendProfileReconciler) listFluxObjects(ctx context.Context, namespace string, selector labels.Selector, gvk schema.GroupVersionKind, kind, pattern, versionField string, limit int64) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}, client.Limit(limit+1)); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return counts, "", ""
		}
		return counts, "ApplicationDiscoveryFailed", err.Error()
	}
	if exceededListLimit(list.GetContinue(), len(list.Items), limit) {
		return backendDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("%s discovery exceeded maxObjects=%d; narrow spec.discovery.selector", kind, limit)
	}
	counts.applications = int32(len(list.Items))
	for i := range list.Items {
		obj := &list.Items[i]
		counts.addSelected(kaprov1alpha2.DiscoveredBackendObject{
			APIVersion:   gvk.GroupVersion().String(),
			Kind:         kind,
			Namespace:    obj.GetNamespace(),
			Name:         obj.GetName(),
			Pattern:      pattern,
			Reason:       "selected Flux promotion target",
			Unit:         backendObjectUnit(obj),
			VersionField: versionField,
		})
	}
	return counts, "", ""
}

func fluxSourceVersionField(obj *unstructured.Unstructured) string {
	for _, field := range []string{"tag", "semver", "digest", "branch"} {
		if value, _, _ := unstructured.NestedString(obj.Object, "spec", "ref", field); value != "" {
			return "spec.ref." + field
		}
	}
	return "spec.ref.tag"
}

func (r *BackendProfileReconciler) profileReadiness(ctx context.Context, profile *kaprov1alpha2.Backend) (bool, string, string) {
	switch profile.Spec.Driver {
	case kaprov1alpha2.BackendDriverFlux, kaprov1alpha2.BackendDriverArgo, kaprov1alpha2.BackendDriverOCI:
		return true, "BuiltInBackendReady", fmt.Sprintf("built-in %s backend is available", profile.Spec.Driver)
	case kaprov1alpha2.BackendDriverExternal:
		if profile.Spec.PluginRef == "" {
			return false, "MissingPluginRef", "external backend requires spec.pluginRef"
		}
		var reg kaprov1alpha2.Plugin
		if err := r.Get(ctx, client.ObjectKey{Name: profile.Spec.PluginRef}, &reg); err != nil {
			return false, "PluginRegistrationNotFound", err.Error()
		}
		if !reg.Status.Ready || reg.Status.ObservedGeneration != reg.Generation {
			return false, "PluginRegistrationNotReady", fmt.Sprintf("plugin registration %q is not ready", profile.Spec.PluginRef)
		}
		return true, "ExternalBackendReady", fmt.Sprintf("external backend plugin %q is ready", profile.Spec.PluginRef)
	default:
		return false, "UnsupportedDriver", fmt.Sprintf("backend driver %q is unsupported", profile.Spec.Driver)
	}
}

func backendDiscoveryListLimit(profile *kaprov1alpha2.Backend) int64 {
	if profile.Spec.Discovery != nil && profile.Spec.Discovery.MaxObjects > 0 {
		return int64(profile.Spec.Discovery.MaxObjects)
	}
	return defaultBackendDiscoveryMaxObjects
}

func exceededListLimit(continueToken string, count int, limit int64) bool {
	return continueToken != "" || int64(count) > limit
}

func (r *BackendProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.Backend{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.backendProfilesForBackendObject),
			builder.WithPredicates(argoClusterSecretPredicate),
		)
	if os.Getenv("KAPRO_ENABLE_BACKEND_OBJECT_WATCHES") == "true" {
		for _, gvk := range backendDiscoveryWatchKinds() {
			b = b.Watches(backendDiscoveryWatchObject(gvk), handler.EnqueueRequestsFromMapFunc(r.backendProfilesForBackendObject))
		}
	}
	return b.Complete(r)
}

func backendDiscoveryWatchKinds() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"},
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationSet"},
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"},
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "OCIRepository"},
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "Bucket"},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"},
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"},
	}
}

func backendDiscoveryWatchObject(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj
}

func (r *BackendProfileReconciler) backendProfilesForBackendObject(ctx context.Context, obj client.Object) []reconcile.Request {
	var profiles kaprov1alpha2.BackendList
	if err := r.List(ctx, &profiles); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(profiles.Items))
	for i := range profiles.Items {
		profile := &profiles.Items[i]
		if backendProfileMatchesObject(profile, obj) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
		}
	}
	return requests
}

func backendProfileMatchesObject(profile *kaprov1alpha2.Backend, obj client.Object) bool {
	if profile.Spec.Discovery == nil || !profile.Spec.Discovery.Enabled {
		return false
	}
	gvk := obj.GetObjectKind().GroupVersionKind()
	var objectDriver kaprov1alpha2.BackendDriver
	switch {
	case isCoreSecretObject(obj):
		if obj.GetLabels()["argocd.argoproj.io/secret-type"] != "cluster" {
			return false
		}
		objectDriver = kaprov1alpha2.BackendDriverArgo
	case gvk.Group == "argoproj.io":
		objectDriver = kaprov1alpha2.BackendDriverArgo
	case strings.HasSuffix(gvk.Group, "toolkit.fluxcd.io"):
		objectDriver = kaprov1alpha2.BackendDriverFlux
	default:
		return false
	}
	if profile.Spec.Driver != objectDriver {
		return false
	}
	namespace := "argocd"
	if profile.Spec.Driver == kaprov1alpha2.BackendDriverFlux {
		namespace = "flux-system"
	}
	if profile.Spec.Parameters["namespace"] != "" {
		namespace = profile.Spec.Parameters["namespace"]
	}
	if obj.GetNamespace() != namespace {
		return false
	}
	if profile.Spec.Discovery.Selector == nil {
		return true
	}
	selector, err := metav1.LabelSelectorAsSelector(profile.Spec.Discovery.Selector)
	if err != nil {
		return true
	}
	return selector.Matches(labels.Set(obj.GetLabels()))
}

func isCoreSecretObject(obj client.Object) bool {
	if _, ok := obj.(*corev1.Secret); ok {
		return true
	}
	gvk := obj.GetObjectKind().GroupVersionKind()
	return gvk.Group == "" && gvk.Kind == "Secret"
}

func (d *backendDiscoveryCounts) addSelected(obj kaprov1alpha2.DiscoveredBackendObject) {
	if len(d.selected) < maxBackendDiscoveryStatusObjects {
		d.selected = append(d.selected, obj)
	}
}

func (d *backendDiscoveryCounts) addSkipped(obj kaprov1alpha2.DiscoveredBackendObject) {
	if len(d.skipped) < maxBackendDiscoveryStatusObjects {
		d.skipped = append(d.skipped, obj)
	}
}

func (d *backendDiscoveryCounts) addUnsupported(obj kaprov1alpha2.DiscoveredBackendObject) {
	if len(d.unsupported) < maxBackendDiscoveryStatusObjects {
		d.unsupported = append(d.unsupported, obj)
	}
}

func (d *backendDiscoveryCounts) merge(other backendDiscoveryCounts) {
	d.clusters += other.clusters
	d.applications += other.applications
	d.applicationSets += other.applicationSets
	for _, obj := range other.selected {
		d.addSelected(obj)
	}
	for _, obj := range other.skipped {
		d.addSkipped(obj)
	}
	for _, obj := range other.unsupported {
		d.addUnsupported(obj)
	}
	d.errors = append(d.errors, other.errors...)
}

func (d backendDiscoveryCounts) summary(driver string) string {
	return fmt.Sprintf("discovered %d %s clusters, %d applications, %d applicationSets, %d sampled selected objects, %d sampled skipped objects, and %d sampled unsupported patterns",
		d.clusters, driver, d.applications, d.applicationSets, len(d.selected), len(d.skipped), len(d.unsupported))
}

func argoApplicationPattern(app *unstructured.Unstructured) string {
	for _, owner := range app.GetOwnerReferences() {
		if owner.Kind == "ApplicationSet" && owner.APIVersion == "argoproj.io/v1alpha1" {
			return "applicationset-child"
		}
	}
	labels := app.GetLabels()
	annotations := app.GetAnnotations()
	for _, key := range []string{"kapro.io/pattern", "pattern", "argocd.argoproj.io/pattern"} {
		if value := labels[key]; value != "" {
			return normalizeBackendPattern(value)
		}
		if value := annotations[key]; value != "" {
			return normalizeBackendPattern(value)
		}
	}
	return "application"
}

func normalizeBackendPattern(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "app-of-apps", "appofapps", "app-of-apps-root", "root":
		return "app-of-apps-root"
	case "app-of-apps-child", "appofapps-child", "child":
		return "app-of-apps-child"
	case "applicationset-child", "appset-child":
		return "applicationset-child"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func argoPromotionUnit(obj *unstructured.Unstructured) string {
	if labels := obj.GetLabels(); labels != nil {
		if service := labels["kapro.io/unit"]; service != "" {
			return service
		}
		if service := labels["service"]; service != "" {
			return service
		}
		if app := labels["app.kubernetes.io/name"]; app != "" {
			return app
		}
	}
	return obj.GetName()
}

func backendObjectUnit(obj *unstructured.Unstructured) string {
	if labels := obj.GetLabels(); labels != nil {
		if unit := labels["kapro.io/unit"]; unit != "" {
			return unit
		}
		if app := labels["app.kubernetes.io/name"]; app != "" {
			return app
		}
	}
	return obj.GetName()
}
