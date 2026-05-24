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

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// argoClusterSecretLabel is the well-known Argo CD label that identifies a
// cluster Secret in the argocd namespace. Used to short-circuit Secret watch
// events at predicate time so unrelated Secret churn doesn't enqueue every
// SubstrateProfile reconcile.
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

const substrateProfileDiscoveryRequeue = 2 * time.Minute

// SubstrateReconciler records readiness for selectable delivery substrates.
type SubstrateReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=substrates,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=substrates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=substrateclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=plugins,verbs=get;list;watch
// +kubebuilder:rbac:groups=argocd.substrate.kapro.io,resources=argocdsubstrateconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=flux.substrate.kapro.io,resources=fluxsubstrateconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubernetes.substrate.kapro.io,resources=kubernetesapplyconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=oci.substrate.kapro.io,resources=ocibundleapplyconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch
// +kubebuilder:rbac:groups=argoproj.io,resources=applicationsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories;ocirepositories;buckets,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch

const maxSubstrateDiscoveryStatusObjects = 128
const defaultSubstrateDiscoveryMaxObjects int64 = 1000

func (r *SubstrateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var profile kaprov1alpha1.Substrate
	if err := r.Get(ctx, req.NamespacedName, &profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	patch := client.MergeFrom(profile.DeepCopy())
	now := metav1.Now()
	ready, reason, message := r.profileReadiness(ctx, &profile)

	profile.Status.ObservedGeneration = profile.Generation
	profile.Status.Ready = ready
	profile.Status.Substrate = profile.Spec.CanonicalSubstrate()
	profile.Status.Execution = r.substrateCanonicalExecution(ctx, &profile)
	profile.Status.ClassName = ""
	profile.Status.ConfigRef = nil
	if profile.Spec.ClassRef != nil {
		profile.Status.ClassName = profile.Spec.ClassRef.Name
	}
	if profile.Spec.ConfigRef != nil {
		configRef := *profile.Spec.ConfigRef
		profile.Status.ConfigRef = &configRef
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
	r.setSubstrateClassConditions(ctx, &profile, now)
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
		apimeta.RemoveStatusCondition(&profile.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	} else {
		apimeta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{
			Type:               kaprov1alpha1.ConditionTypeStalled,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: profile.Generation,
			LastTransitionTime: now,
		})
	}

	if err := r.Status().Patch(ctx, &profile, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch SubstrateProfile status: %w", err)
	}
	if profile.Spec.Discovery != nil && profile.Spec.Discovery.Enabled {
		return ctrl.Result{RequeueAfter: substrateProfileDiscoveryRequeue}, nil
	}
	return ctrl.Result{}, nil
}

type substrateDiscoveryCounts struct {
	clusters        int32
	applications    int32
	applicationSets int32
	status          metav1.ConditionStatus
	selected        []kaprov1alpha1.DiscoveredSubstrateObject
	skipped         []kaprov1alpha1.DiscoveredSubstrateObject
	unsupported     []kaprov1alpha1.DiscoveredSubstrateObject
	errors          []string
}

func (r *SubstrateReconciler) observeDiscovery(ctx context.Context, profile *kaprov1alpha1.Substrate) (substrateDiscoveryCounts, string, string) {
	counts := substrateDiscoveryCounts{status: metav1.ConditionTrue}
	if profile.Spec.Discovery == nil || !profile.Spec.Discovery.Enabled {
		return counts, "DiscoveryDisabled", "substrate discovery is disabled"
	}
	namespace := "argocd"
	if profile.Spec.SubstrateKind() == string(kaprov1alpha1.SubstrateDriverFlux) {
		namespace = "flux-system"
	}
	if profile.Spec.Parameters["namespace"] != "" {
		namespace = profile.Spec.Parameters["namespace"]
	}

	switch profile.Spec.SubstrateKind() {
	case string(kaprov1alpha1.SubstrateDriverArgo):
		return r.observeArgoDiscovery(ctx, profile, namespace)
	case string(kaprov1alpha1.SubstrateDriverFlux):
		return r.observeFluxDiscovery(ctx, profile, namespace)
	default:
		return counts, "DiscoveryUnsupported", fmt.Sprintf("discovery is not implemented for %s substrates", profile.Spec.SubstrateKind())
	}
}

func (r *SubstrateReconciler) observeArgoDiscovery(ctx context.Context, profile *kaprov1alpha1.Substrate, namespace string) (substrateDiscoveryCounts, string, string) {
	counts := substrateDiscoveryCounts{status: metav1.ConditionTrue}
	selector := labels.Everything()
	if profile.Spec.Discovery.Selector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(profile.Spec.Discovery.Selector)
		if err != nil {
			return substrateDiscoveryCounts{status: metav1.ConditionFalse}, "InvalidSelector", err.Error()
		}
	}
	limit := substrateDiscoveryListLimit(profile)
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList,
		client.InNamespace(namespace),
		client.MatchingLabels{"argocd.argoproj.io/secret-type": "cluster"},
		client.Limit(limit+1),
	); err != nil {
		return substrateDiscoveryCounts{status: metav1.ConditionFalse}, "ClusterDiscoveryFailed", err.Error()
	}
	if exceededListLimit(secretList.Continue, len(secretList.Items), limit) {
		return substrateDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("Argo CD cluster Secret discovery exceeded maxObjects=%d; narrow spec.discovery.selector", limit)
	}
	for _, secret := range secretList.Items {
		if selector.Matches(labels.Set(secret.Labels)) {
			counts.clusters++
			counts.addSelected(kaprov1alpha1.DiscoveredSubstrateObject{
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
		return substrateDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("Argo CD Application discovery exceeded maxObjects=%d; narrow spec.discovery.selector", limit)
	}
	counts.applications = int32(len(appList.Items))
	for i := range appList.Items {
		app := &appList.Items[i]
		pattern := argoApplicationPattern(app)
		entry := kaprov1alpha1.DiscoveredSubstrateObject{
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
			return substrateDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
				fmt.Sprintf("Argo CD ApplicationSet discovery exceeded maxObjects=%d; narrow spec.discovery.selector", limit)
		}
		counts.applicationSets = int32(len(appSetList.Items))
		for i := range appSetList.Items {
			appSet := &appSetList.Items[i]
			counts.addSkipped(kaprov1alpha1.DiscoveredSubstrateObject{
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

func (r *SubstrateReconciler) observeFluxDiscovery(ctx context.Context, profile *kaprov1alpha1.Substrate, namespace string) (substrateDiscoveryCounts, string, string) {
	counts := substrateDiscoveryCounts{status: metav1.ConditionTrue}
	appSelector := labels.Everything()
	if profile.Spec.Discovery.Selector != nil {
		var err error
		appSelector, err = metav1.LabelSelectorAsSelector(profile.Spec.Discovery.Selector)
		if err != nil {
			return counts, "InvalidSelector", err.Error()
		}
	}
	limit := substrateDiscoveryListLimit(profile)

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

func (r *SubstrateReconciler) listFluxSourceObjects(ctx context.Context, namespace string, selector labels.Selector, gvk schema.GroupVersionKind, kind, pattern string, limit int64) (substrateDiscoveryCounts, string, string) {
	counts := substrateDiscoveryCounts{status: metav1.ConditionTrue}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}, client.Limit(limit+1)); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return counts, "", ""
		}
		return counts, "SourceDiscoveryFailed", err.Error()
	}
	if exceededListLimit(list.GetContinue(), len(list.Items), limit) {
		return substrateDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("%s discovery exceeded maxObjects=%d; narrow spec.discovery.selector", kind, limit)
	}
	counts.applications = int32(len(list.Items))
	for i := range list.Items {
		obj := &list.Items[i]
		counts.addSelected(kaprov1alpha1.DiscoveredSubstrateObject{
			APIVersion:   gvk.GroupVersion().String(),
			Kind:         kind,
			Namespace:    obj.GetNamespace(),
			Name:         obj.GetName(),
			Pattern:      pattern,
			Reason:       "selected Flux source revision target",
			Unit:         substrateObjectUnit(obj),
			VersionField: fluxSourceVersionField(obj),
		})
	}
	return counts, "", ""
}

func (r *SubstrateReconciler) listFluxObjects(ctx context.Context, namespace string, selector labels.Selector, gvk schema.GroupVersionKind, kind, pattern, versionField string, limit int64) (substrateDiscoveryCounts, string, string) {
	counts := substrateDiscoveryCounts{status: metav1.ConditionTrue}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}, client.Limit(limit+1)); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return counts, "", ""
		}
		return counts, "ApplicationDiscoveryFailed", err.Error()
	}
	if exceededListLimit(list.GetContinue(), len(list.Items), limit) {
		return substrateDiscoveryCounts{status: metav1.ConditionFalse}, "DiscoveryLimitExceeded",
			fmt.Sprintf("%s discovery exceeded maxObjects=%d; narrow spec.discovery.selector", kind, limit)
	}
	counts.applications = int32(len(list.Items))
	for i := range list.Items {
		obj := &list.Items[i]
		counts.addSelected(kaprov1alpha1.DiscoveredSubstrateObject{
			APIVersion:   gvk.GroupVersion().String(),
			Kind:         kind,
			Namespace:    obj.GetNamespace(),
			Name:         obj.GetName(),
			Pattern:      pattern,
			Reason:       "selected Flux promotion target",
			Unit:         substrateObjectUnit(obj),
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

func (r *SubstrateReconciler) substrateCanonicalExecution(ctx context.Context, profile *kaprov1alpha1.Substrate) *kaprov1alpha1.SubstrateExecutionSpec {
	if profile.Spec.ClassRef == nil || profile.Spec.ClassRef.Name == "" {
		return profile.Spec.CanonicalExecution()
	}
	class, ok, _, _ := r.resolveSubstrateClass(ctx, profile)
	if !ok {
		return profile.Spec.CanonicalExecution()
	}
	return &kaprov1alpha1.SubstrateExecutionSpec{Mode: substrateExecutionModeForClass(profile, class)}
}

func (r *SubstrateReconciler) profileReadiness(ctx context.Context, profile *kaprov1alpha1.Substrate) (bool, string, string) {
	if profile.Spec.ClassRef != nil && profile.Spec.ClassRef.Name != "" {
		return r.profileClassReadiness(ctx, profile)
	}
	kind := profile.Spec.SubstrateKind()
	switch kind {
	case string(kaprov1alpha1.SubstrateDriverFlux), string(kaprov1alpha1.SubstrateDriverArgo), string(kaprov1alpha1.SubstrateDriverOCI):
		return true, "BuiltInSubstrateReady", fmt.Sprintf("built-in %s substrate is available", kind)
	case string(kaprov1alpha1.SubstrateDriverExternal):
		if profile.Spec.PluginRef == "" {
			return false, "MissingPluginRef", "external substrate requires spec.pluginRef"
		}
		var reg kaprov1alpha1.Plugin
		if err := r.Get(ctx, client.ObjectKey{Name: profile.Spec.PluginRef}, &reg); err != nil {
			return false, "PluginRegistrationNotFound", err.Error()
		}
		if !reg.Status.Ready || reg.Status.ObservedGeneration != reg.Generation {
			return false, "PluginRegistrationNotReady", fmt.Sprintf("plugin registration %q is not ready", profile.Spec.PluginRef)
		}
		return true, "ExternalSubstrateReady", fmt.Sprintf("external substrate plugin %q is ready", profile.Spec.PluginRef)
	default:
		// Open substrates are admitted before their actuator exists so GitOps
		// users can commit Substrate YAML ahead of deploying plugin code.
		return false, "ActuatorNotRegistered", fmt.Sprintf("substrate.kind=%s has no registered built-in actuator; create the actuator binding before promotion", kind)
	}
}

func (r *SubstrateReconciler) profileClassReadiness(ctx context.Context, profile *kaprov1alpha1.Substrate) (bool, string, string) {
	class, ok, reason, message := r.resolveSubstrateClass(ctx, profile)
	if !ok {
		return false, reason, message
	}
	if ready, reason, message := substrateClassAccepted(class); !ready {
		return false, reason, message
	}
	mode := substrateExecutionModeForClass(profile, class)
	if !substrateClassSupportsExecutionMode(class, mode) {
		return false, "ExecutionModeUnsupported", fmt.Sprintf("SubstrateClass %q does not support execution mode %q", class.Name, mode)
	}
	if profile.Spec.ConfigRef == nil {
		if len(class.Status.AcceptedConfigKinds) > 0 {
			return false, "MissingConfigRef", fmt.Sprintf("SubstrateClass %q requires Substrate.spec.configRef", class.Name)
		}
		return true, "SubstrateClassSubstrateReady", fmt.Sprintf("SubstrateClass %q is ready", class.Name)
	}
	if ok, reason, message := r.substrateConfigAccepted(ctx, class, profile.Spec.ConfigRef); !ok {
		return false, reason, message
	}
	return true, "SubstrateClassSubstrateReady", fmt.Sprintf("SubstrateClass %q and config %s/%s are ready", class.Name, profile.Spec.ConfigRef.APIVersion, profile.Spec.ConfigRef.Kind)
}

func (r *SubstrateReconciler) setSubstrateClassConditions(ctx context.Context, profile *kaprov1alpha1.Substrate, now metav1.Time) {
	if profile.Spec.ClassRef == nil || profile.Spec.ClassRef.Name == "" {
		apimeta.RemoveStatusCondition(&profile.Status.Conditions, "ClassAccepted")
		apimeta.RemoveStatusCondition(&profile.Status.Conditions, "ConfigAccepted")
		return
	}
	class, classOK, reason, message := r.resolveSubstrateClass(ctx, profile)
	if classOK {
		classOK, reason, message = substrateClassAccepted(class)
	}
	classStatus := metav1.ConditionFalse
	if classOK {
		classStatus = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{
		Type:               "ClassAccepted",
		Status:             classStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: profile.Generation,
		LastTransitionTime: now,
	})

	if profile.Spec.ConfigRef == nil {
		if classOK && len(class.Status.AcceptedConfigKinds) > 0 {
			apimeta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{
				Type:               "ConfigAccepted",
				Status:             metav1.ConditionFalse,
				Reason:             "MissingConfigRef",
				Message:            fmt.Sprintf("SubstrateClass %q requires Substrate.spec.configRef", class.Name),
				ObservedGeneration: profile.Generation,
				LastTransitionTime: now,
			})
			return
		}
		apimeta.RemoveStatusCondition(&profile.Status.Conditions, "ConfigAccepted")
		return
	}
	configOK := false
	configReason := "ClassNotAccepted"
	configMessage := "SubstrateClass must be accepted before configRef can be validated"
	if classOK {
		configOK, configReason, configMessage = r.substrateConfigAccepted(ctx, class, profile.Spec.ConfigRef)
	}
	configStatus := metav1.ConditionFalse
	if configOK {
		configStatus = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{
		Type:               "ConfigAccepted",
		Status:             configStatus,
		Reason:             configReason,
		Message:            configMessage,
		ObservedGeneration: profile.Generation,
		LastTransitionTime: now,
	})
}

func (r *SubstrateReconciler) resolveSubstrateClass(ctx context.Context, profile *kaprov1alpha1.Substrate) (*kaprov1alpha1.SubstrateClass, bool, string, string) {
	if profile.Spec.ClassRef == nil || profile.Spec.ClassRef.Name == "" {
		return nil, false, "MissingClassRef", "Substrate.spec.classRef.name is required"
	}
	var class kaprov1alpha1.SubstrateClass
	if err := r.Get(ctx, client.ObjectKey{Name: profile.Spec.ClassRef.Name}, &class); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, "SubstrateClassNotFound", fmt.Sprintf("SubstrateClass %q was not found", profile.Spec.ClassRef.Name)
		}
		return nil, false, "SubstrateClassLookupFailed", err.Error()
	}
	return &class, true, "SubstrateClassAccepted", fmt.Sprintf("SubstrateClass %q was found", class.Name)
}

func substrateClassAccepted(class *kaprov1alpha1.SubstrateClass) (bool, string, string) {
	if class.Status.ObservedGeneration != class.Generation {
		return false, "SubstrateClassNotObserved", fmt.Sprintf("SubstrateClass %q has not observed generation %d", class.Name, class.Generation)
	}
	accepted := apimeta.FindStatusCondition(class.Status.Conditions, "Accepted")
	if accepted == nil {
		return false, "SubstrateClassNotAccepted", fmt.Sprintf("SubstrateClass %q has no Accepted condition", class.Name)
	}
	if accepted.Status != metav1.ConditionTrue {
		reason := accepted.Reason
		if reason == "" {
			reason = "SubstrateClassNotAccepted"
		}
		return false, reason, accepted.Message
	}
	return true, "SubstrateClassAccepted", fmt.Sprintf("SubstrateClass %q is accepted", class.Name)
}

func substrateExecutionModeForClass(profile *kaprov1alpha1.Substrate, class *kaprov1alpha1.SubstrateClass) kaprov1alpha1.ExecutionMode {
	if profile.Spec.Execution != nil && profile.Spec.Execution.Mode != "" {
		return profile.Spec.Execution.Mode
	}
	if class != nil && class.Spec.ExecutionModes != nil && class.Spec.ExecutionModes.Default != "" {
		return class.Spec.ExecutionModes.Default
	}
	return profile.Spec.ExecutionMode()
}

func substrateClassSupportsExecutionMode(class *kaprov1alpha1.SubstrateClass, mode kaprov1alpha1.ExecutionMode) bool {
	if class.Status.ExecutionModes == nil || len(class.Status.ExecutionModes.Supported) == 0 {
		return true
	}
	for _, supported := range class.Status.ExecutionModes.Supported {
		if supported == mode {
			return true
		}
	}
	return false
}

func (r *SubstrateReconciler) substrateConfigAccepted(ctx context.Context, class *kaprov1alpha1.SubstrateClass, ref *kaprov1alpha1.SubstrateObjectReference) (bool, string, string) {
	if ref == nil {
		return true, "ConfigRefNotRequired", "Substrate does not reference a typed substrate config"
	}
	if !substrateClassAcceptsConfigKind(class, ref) {
		return false, "ConfigKindNotAccepted", fmt.Sprintf("SubstrateClass %q does not accept %s/%s", class.Name, ref.APIVersion, ref.Kind)
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return false, "InvalidConfigRef", err.Error()
	}
	config := &unstructured.Unstructured{}
	config.SetGroupVersionKind(gv.WithKind(ref.Kind))
	if err := r.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, config); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "ConfigNotFound", fmt.Sprintf("%s/%s %q was not found", ref.APIVersion, ref.Kind, ref.Name)
		}
		return false, "ConfigLookupFailed", err.Error()
	}
	return true, "ConfigAccepted", fmt.Sprintf("%s/%s %q is accepted", ref.APIVersion, ref.Kind, ref.Name)
}

func substrateClassAcceptsConfigKind(class *kaprov1alpha1.SubstrateClass, ref *kaprov1alpha1.SubstrateObjectReference) bool {
	for _, accepted := range class.Status.AcceptedConfigKinds {
		if accepted.APIVersion == ref.APIVersion && accepted.Kind == ref.Kind {
			return true
		}
	}
	return false
}

func substrateDiscoveryListLimit(profile *kaprov1alpha1.Substrate) int64 {
	if profile.Spec.Discovery != nil && profile.Spec.Discovery.MaxObjects > 0 {
		return int64(profile.Spec.Discovery.MaxObjects)
	}
	return defaultSubstrateDiscoveryMaxObjects
}

func exceededListLimit(continueToken string, count int, limit int64) bool {
	return continueToken != "" || int64(count) > limit
}

func (r *SubstrateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Substrate{}).
		Watches(
			&kaprov1alpha1.SubstrateClass{},
			handler.EnqueueRequestsFromMapFunc(r.substrateProfilesForSubstrateClass),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.substrateProfilesForSubstrateObject),
			builder.WithPredicates(argoClusterSecretPredicate),
		)
	for _, gvk := range typedSubstrateConfigWatchKinds() {
		b = b.Watches(substrateDiscoveryWatchObject(gvk), handler.EnqueueRequestsFromMapFunc(r.substrateProfilesForTypedConfig))
	}
	if os.Getenv("KAPRO_ENABLE_SUBSTRATE_OBJECT_WATCHES") == "true" {
		for _, gvk := range substrateDiscoveryWatchKinds() {
			b = b.Watches(substrateDiscoveryWatchObject(gvk), handler.EnqueueRequestsFromMapFunc(r.substrateProfilesForSubstrateObject))
		}
	}
	return b.Complete(r)
}

func typedSubstrateConfigWatchKinds() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{
		{Group: "argocd.substrate.kapro.io", Version: "v1alpha1", Kind: "ArgoCDSubstrateConfig"},
		{Group: "flux.substrate.kapro.io", Version: "v1alpha1", Kind: "FluxSubstrateConfig"},
		{Group: "kubernetes.substrate.kapro.io", Version: "v1alpha1", Kind: "KubernetesApplyConfig"},
		{Group: "oci.substrate.kapro.io", Version: "v1alpha1", Kind: "OCIBundleApplyConfig"},
	}
}

func substrateDiscoveryWatchKinds() []schema.GroupVersionKind {
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

func substrateDiscoveryWatchObject(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj
}

func (r *SubstrateReconciler) substrateProfilesForSubstrateObject(ctx context.Context, obj client.Object) []reconcile.Request {
	var profiles kaprov1alpha1.SubstrateList
	if err := r.List(ctx, &profiles); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(profiles.Items))
	for i := range profiles.Items {
		profile := &profiles.Items[i]
		if substrateProfileMatchesObject(profile, obj) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
		}
	}
	return requests
}

func (r *SubstrateReconciler) substrateProfilesForSubstrateClass(ctx context.Context, obj client.Object) []reconcile.Request {
	var profiles kaprov1alpha1.SubstrateList
	if err := r.List(ctx, &profiles); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(profiles.Items))
	for i := range profiles.Items {
		profile := &profiles.Items[i]
		if profile.Spec.ClassRef != nil && profile.Spec.ClassRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
		}
	}
	return requests
}

func (r *SubstrateReconciler) substrateProfilesForTypedConfig(ctx context.Context, obj client.Object) []reconcile.Request {
	var profiles kaprov1alpha1.SubstrateList
	if err := r.List(ctx, &profiles); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(profiles.Items))
	gvk := obj.GetObjectKind().GroupVersionKind()
	apiVersion := gvk.GroupVersion().String()
	for i := range profiles.Items {
		profile := &profiles.Items[i]
		ref := profile.Spec.ConfigRef
		if ref == nil {
			continue
		}
		if ref.APIVersion == apiVersion && ref.Kind == gvk.Kind && ref.Namespace == obj.GetNamespace() && ref.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
		}
	}
	return requests
}

func substrateProfileMatchesObject(profile *kaprov1alpha1.Substrate, obj client.Object) bool {
	if profile.Spec.Discovery == nil || !profile.Spec.Discovery.Enabled {
		return false
	}
	gvk := obj.GetObjectKind().GroupVersionKind()
	var objectDriver kaprov1alpha1.SubstrateDriver
	switch {
	case isCoreSecretObject(obj):
		if obj.GetLabels()["argocd.argoproj.io/secret-type"] != "cluster" {
			return false
		}
		objectDriver = kaprov1alpha1.SubstrateDriverArgo
	case gvk.Group == "argoproj.io":
		objectDriver = kaprov1alpha1.SubstrateDriverArgo
	case strings.HasSuffix(gvk.Group, "toolkit.fluxcd.io"):
		objectDriver = kaprov1alpha1.SubstrateDriverFlux
	default:
		return false
	}
	if profile.Spec.SubstrateKind() != string(objectDriver) {
		return false
	}
	namespace := "argocd"
	if profile.Spec.SubstrateKind() == string(kaprov1alpha1.SubstrateDriverFlux) {
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

func (d *substrateDiscoveryCounts) addSelected(obj kaprov1alpha1.DiscoveredSubstrateObject) {
	if len(d.selected) < maxSubstrateDiscoveryStatusObjects {
		d.selected = append(d.selected, obj)
	}
}

func (d *substrateDiscoveryCounts) addSkipped(obj kaprov1alpha1.DiscoveredSubstrateObject) {
	if len(d.skipped) < maxSubstrateDiscoveryStatusObjects {
		d.skipped = append(d.skipped, obj)
	}
}

func (d *substrateDiscoveryCounts) addUnsupported(obj kaprov1alpha1.DiscoveredSubstrateObject) {
	if len(d.unsupported) < maxSubstrateDiscoveryStatusObjects {
		d.unsupported = append(d.unsupported, obj)
	}
}

func (d *substrateDiscoveryCounts) merge(other substrateDiscoveryCounts) {
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

func (d substrateDiscoveryCounts) summary(driver string) string {
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
			return normalizeSubstratePattern(value)
		}
		if value := annotations[key]; value != "" {
			return normalizeSubstratePattern(value)
		}
	}
	return "application"
}

func normalizeSubstratePattern(value string) string {
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

func substrateObjectUnit(obj *unstructured.Unstructured) string {
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
