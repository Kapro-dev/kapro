package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

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
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch

func (r *BackendProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var profile kaprov1alpha1.BackendProfile
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
		profile.Status.Runtime = kaprov1alpha1.BackendRuntimeBoth
	}
	discovery, discoveryReason, discoveryMessage := r.observeDiscovery(ctx, &profile)
	profile.Status.DiscoveredClusters = discovery.clusters
	profile.Status.DiscoveredApplications = discovery.applications

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
		return ctrl.Result{}, fmt.Errorf("patch BackendProfile status: %w", err)
	}
	return ctrl.Result{}, nil
}

type backendDiscoveryCounts struct {
	clusters     int32
	applications int32
	status       metav1.ConditionStatus
}

func (r *BackendProfileReconciler) observeDiscovery(ctx context.Context, profile *kaprov1alpha1.BackendProfile) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	if profile.Spec.Discovery == nil || !profile.Spec.Discovery.Enabled {
		return counts, "DiscoveryDisabled", "backend discovery is disabled"
	}
	namespace := "argocd"
	if profile.Spec.Driver == kaprov1alpha1.BackendDriverFlux {
		namespace = "flux-system"
	}
	if profile.Spec.Parameters["namespace"] != "" {
		namespace = profile.Spec.Parameters["namespace"]
	}

	switch profile.Spec.Driver {
	case kaprov1alpha1.BackendDriverArgo:
		return r.observeArgoDiscovery(ctx, profile, namespace)
	case kaprov1alpha1.BackendDriverFlux:
		return r.observeFluxDiscovery(ctx, profile, namespace)
	default:
		return counts, "DiscoveryUnsupported", fmt.Sprintf("discovery is not implemented for %s backends", profile.Spec.Driver)
	}
}

func (r *BackendProfileReconciler) observeArgoDiscovery(ctx context.Context, profile *kaprov1alpha1.BackendProfile, namespace string) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	selector := labels.Everything()
	if profile.Spec.Discovery.Selector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(profile.Spec.Discovery.Selector)
		if err != nil {
			return backendDiscoveryCounts{status: metav1.ConditionFalse}, "InvalidSelector", err.Error()
		}
	}
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList,
		client.InNamespace(namespace),
		client.MatchingLabels{"argocd.argoproj.io/secret-type": "cluster"},
	); err != nil {
		return backendDiscoveryCounts{status: metav1.ConditionFalse}, "ClusterDiscoveryFailed", err.Error()
	}
	for _, secret := range secretList.Items {
		if selector.Matches(labels.Set(secret.Labels)) {
			counts.clusters++
		}
	}

	appList := &unstructured.UnstructuredList{}
	appList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList",
	})
	if err := r.List(ctx, appList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return counts, "ApplicationDiscoveryUnavailable", "Argo CD Application CRD is not installed"
		}
		return counts, "ApplicationDiscoveryFailed", err.Error()
	}
	counts.applications = int32(len(appList.Items))
	return counts, "DiscoverySucceeded", fmt.Sprintf("discovered %d Argo clusters and %d applications", counts.clusters, counts.applications)
}

func (r *BackendProfileReconciler) observeFluxDiscovery(ctx context.Context, profile *kaprov1alpha1.BackendProfile, namespace string) (backendDiscoveryCounts, string, string) {
	counts := backendDiscoveryCounts{status: metav1.ConditionTrue}
	appSelector := labels.Everything()
	if profile.Spec.Discovery.Selector != nil {
		var err error
		appSelector, err = metav1.LabelSelectorAsSelector(profile.Spec.Discovery.Selector)
		if err != nil {
			return counts, "InvalidSelector", err.Error()
		}
	}

	helmReleases, reason, message := r.countUnstructured(ctx, namespace, appSelector, schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList",
	})
	if reason != "" {
		return counts, reason, message
	}
	kustomizations, reason, message := r.countUnstructured(ctx, namespace, appSelector, schema.GroupVersionKind{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList",
	})
	if reason != "" {
		return counts, reason, message
	}
	counts.applications = helmReleases + kustomizations
	return counts, "DiscoverySucceeded", fmt.Sprintf("discovered %d Flux workload objects", counts.applications)
}

func (r *BackendProfileReconciler) countUnstructured(ctx context.Context, namespace string, selector labels.Selector, gvk schema.GroupVersionKind) (int32, string, string) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return 0, "", ""
		}
		return 0, "ApplicationDiscoveryFailed", err.Error()
	}
	return int32(len(list.Items)), "", ""
}

func (r *BackendProfileReconciler) profileReadiness(ctx context.Context, profile *kaprov1alpha1.BackendProfile) (bool, string, string) {
	switch profile.Spec.Driver {
	case kaprov1alpha1.BackendDriverFlux, kaprov1alpha1.BackendDriverArgo:
		return true, "BuiltInBackendReady", fmt.Sprintf("built-in %s backend is available", profile.Spec.Driver)
	case kaprov1alpha1.BackendDriverExternal:
		if profile.Spec.PluginRef == "" {
			return false, "MissingPluginRef", "external backend requires spec.pluginRef"
		}
		var reg kaprov1alpha1.PluginRegistration
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

func (r *BackendProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.BackendProfile{}).
		Complete(r)
}
