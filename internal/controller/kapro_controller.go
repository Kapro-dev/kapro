package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/provider"
)

var kaproResourceSetGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSet",
}

// KaproReconciler generates hub-side resources from a Kapro + KaproApp spec.
// It produces (all on the hub cluster):
//   - MemberCluster CRs (one per cluster in the fleet)
//   - A Pipeline CR (from kapro.spec.pipeline)
//   - A ResourceSet (Flux Operator) with HelmRelease templates per component
//
// The ResourceSet contains per-cluster inputs with component versions and
// merged Helm values (KaproApp defaults + overrides). Flux Operator distributes
// the rendered HelmReleases to spokes. Kapro never writes to spoke clusters.
//
// Version promotion is handled by the FluxOperatorActuator (via Release),
// which patches ResourceSet inputs — not by this controller.
type KaproReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

func (r *KaproReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var kapro kaprov1alpha1.Kapro
	if err := r.Get(ctx, req.NamespacedName, &kapro); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if kapro.Spec.Suspended {
		l.Info("Kapro is suspended, skipping")
		return ctrl.Result{}, nil
	}

	l.Info("reconciling Kapro", "name", kapro.Name)

	// Resolve the KaproApp.
	var app kaprov1alpha1.KaproApp
	if err := r.Get(ctx, client.ObjectKey{Name: kapro.Spec.AppRef}, &app); err != nil {
		patch := client.MergeFrom(kapro.DeepCopy())
		apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: kapro.Generation,
			Reason:             "KaproAppNotFound",
			Message:            fmt.Sprintf("KaproApp %q not found: %v", kapro.Spec.AppRef, err),
		})
		_ = r.Status().Patch(ctx, &kapro, patch)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	var inventory []string

	// 1. For gcp/gcp-fleet clusters, auto-generate kubeconfig secrets.
	for i := range kapro.Spec.Clusters {
		cluster := &kapro.Spec.Clusters[i]
		if cluster.Provider != "gcp" && cluster.Provider != "gcp-fleet" {
			continue
		}
		secretName, err := r.ensureKubeconfigSecret(ctx, &kapro, cluster)
		if err != nil {
			l.Error(err, "failed to generate kubeconfig secret", "cluster", cluster.Name)
			continue
		}
		cluster.KubeconfigSecret = secretName
	}

	// 2. Generate MemberClusters on the hub.
	for _, cluster := range kapro.Spec.Clusters {
		mc := &kaprov1alpha1.MemberCluster{
			TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "MemberCluster"},
			ObjectMeta: metav1.ObjectMeta{
				Name:   cluster.Name,
				Labels: cluster.Labels,
			},
			Spec: kaprov1alpha1.MemberClusterSpec{
				Actuator: kaprov1alpha1.ActuatorSpec{
					Type: "flux-operator",
					FluxOperator: &kaprov1alpha1.FluxOperatorConfig{
						ResourceSet: kapro.Name + "-workloads",
						Namespace:   "flux-system",
						InputField:  "version",
						TenantField: "tenant",
					},
				},
			},
		}
		if err := r.Patch(ctx, mc,
			client.Apply,
			client.FieldOwner("kapro-controller"),
			client.ForceOwnership,
		); err != nil {
			l.Error(err, "failed to apply MemberCluster", "cluster", cluster.Name)
		}
		inventory = append(inventory, "MemberCluster/"+cluster.Name)
	}

	// 2. Generate Pipeline on the hub.
	pipeline := r.buildPipeline(&kapro)
	if err := r.Patch(ctx, pipeline,
		client.Apply,
		client.FieldOwner("kapro-controller"),
		client.ForceOwnership,
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply Pipeline: %w", err)
	}
	inventory = append(inventory, "Pipeline/"+kapro.Name+"-pipeline")

	// 3. Generate ResourceSet on the hub (Flux Operator distributes to spokes).
	rs := r.buildResourceSet(&kapro, &app)
	if err := r.Patch(ctx, rs,
		client.Apply,
		client.FieldOwner("kapro-controller"),
		client.ForceOwnership,
	); err != nil {
		// If Flux Operator CRD is not installed, set status and requeue slowly
		// instead of crash-looping.
		if isNoMatchError(err) {
			l.Info("Flux Operator CRD (ResourceSet) not found — install Flux Operator on the hub")
			patch := client.MergeFrom(kapro.DeepCopy())
			apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				ObservedGeneration: kapro.Generation,
				Reason:             "FluxOperatorNotInstalled",
				Message:            "ResourceSet CRD not found. Install Flux Operator on the hub cluster.",
			})
			_ = r.Status().Patch(ctx, &kapro, patch)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("apply ResourceSet: %w", err)
	}
	inventory = append(inventory, "ResourceSet/"+kapro.Name+"-workloads")

	// 4. Clean up orphaned resources for removed clusters.
	r.cleanupRemovedClusters(ctx, &kapro)

	// 5. Update Kapro status.
	patch := client.MergeFrom(kapro.DeepCopy())
	kapro.Status.ClusterCount = int32(len(kapro.Spec.Clusters))
	kapro.Status.ComponentCount = int32(len(app.Spec.Components))
	kapro.Status.ObservedGeneration = kapro.Generation
	kapro.Status.Inventory = inventory
	apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: kapro.Generation,
		Reason:             "ReconcileSuccess",
		Message:            fmt.Sprintf("Generated ResourceSet with %d clusters × %d components", len(kapro.Spec.Clusters), len(app.Spec.Components)),
	})
	if err := r.Status().Patch(ctx, &kapro, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Kapro status: %w", err)
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *KaproReconciler) buildPipeline(kapro *kaprov1alpha1.Kapro) *kaprov1alpha1.Pipeline {
	stages := make([]kaprov1alpha1.Stage, 0, len(kapro.Spec.Pipeline.Stages))
	for _, s := range kapro.Spec.Pipeline.Stages {
		stage := kaprov1alpha1.Stage{
			Name: s.Name,
			Selector: metav1.LabelSelector{
				MatchLabels: s.Selector,
			},
			DependsOn: s.DependsOn,
		}
		if s.Gate != nil {
			stage.Gate = s.Gate
		}
		stages = append(stages, stage)
	}
	return &kaprov1alpha1.Pipeline{
		TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Pipeline"},
		ObjectMeta: metav1.ObjectMeta{
			Name: kapro.Name + "-pipeline",
		},
		Spec: kaprov1alpha1.PipelineSpec{Stages: stages},
	}
}

// buildResourceSet creates a Flux Operator ResourceSet on the hub.
// It contains:
//   - inputs[]: one entry per cluster with tenant name + per-component versions + merged values
//   - resources[]: HelmRelease template per component using << inputs.X >> substitution
//
// Flux Operator renders one set of HelmReleases per input entry and distributes
// them to the matching spoke cluster.
func (r *KaproReconciler) buildResourceSet(kapro *kaprov1alpha1.Kapro, app *kaprov1alpha1.KaproApp) *unstructured.Unstructured {
	// Build inputs: one entry per cluster.
	// The "version" field is the primary version promoted by Release.
	// For single-component apps, it's the component version.
	// For multi-component apps, it's the Release.spec.version (bundle version).
	// Per-component versions are embedded in the values_override JSON.
	primaryVersion := app.Spec.Components[0].Version
	inputs := make([]interface{}, 0, len(kapro.Spec.Clusters))
	for _, cluster := range kapro.Spec.Clusters {
		input := map[string]interface{}{
			"tenant":  cluster.Name,
			"version": primaryVersion,
		}
		// Set kubeconfig secret name for cross-cluster delivery.
		// If the cluster has a kubeconfigSecret, Flux helm-controller uses it
		// to deploy HelmReleases to the remote spoke instead of locally.
		if cluster.KubeconfigSecret != "" {
			input["kubeconfig_secret"] = cluster.KubeconfigSecret
		}
		// Merge defaults + per-component versions + matching overrides.
		mergedValues := r.mergeValues(app, cluster.Name, cluster.Labels)
		if mergedValues != "" {
			input["values_override"] = mergedValues
		}
		inputs = append(inputs, input)
	}

	// Build HelmRelease template resources — one per component.
	resources := make([]interface{}, 0, len(app.Spec.Components))
	for _, comp := range app.Spec.Components {
		ns := comp.Namespace
		if ns == "" {
			ns = "flux-system"
		}
		chartName := comp.Name
		if comp.Chart != nil && comp.Chart.Name != "" {
			chartName = comp.Chart.Name
		}

		hrSpec := map[string]interface{}{
			"interval": "10m",
			"chart": map[string]interface{}{
				"spec": map[string]interface{}{
					"chart":   chartName,
					"version": "<< inputs.version >>",
					"sourceRef": map[string]interface{}{
						"kind": "HelmRepository",
						"name": kapro.Name + "-registry",
					},
				},
			},
			"targetNamespace": ns,
			"releaseName":     comp.Name,
			"install": map[string]interface{}{
				"timeout":     "5m",
				"remediation": map[string]interface{}{"retries": 3},
			},
			"upgrade": map[string]interface{}{
				"timeout":     "5m",
				"remediation": map[string]interface{}{"retries": 3},
			},
		}
		// When clusters have kubeconfigSecret, add kubeConfig to HelmRelease
		// so Flux helm-controller deploys to the remote spoke, not locally.
		if hasKubeconfigClusters(kapro) {
			hrSpec["kubeConfig"] = map[string]interface{}{
				"secretRef": map[string]interface{}{
					"name": "<< inputs.kubeconfig_secret >>",
				},
			}
		}
		helmRelease := map[string]interface{}{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]interface{}{
				"name":      comp.Name + "-<< inputs.tenant >>",
				"namespace": "flux-system",
			},
			"spec": hrSpec,
		}
		resources = append(resources, helmRelease)
	}

	// Build the HelmRepository source resource (registry config).
	helmRepo := map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "HelmRepository",
		"metadata": map[string]interface{}{
			"name":      kapro.Name + "-registry",
			"namespace": "flux-system",
		},
		"spec": map[string]interface{}{
			"type":     "oci",
			"interval": "5m",
			"url":      kapro.Spec.Registry.URL,
			"provider": kapro.Spec.Registry.Provider,
		},
	}
	resources = append(resources, helmRepo)

	rs := &unstructured.Unstructured{}
	rs.SetGroupVersionKind(kaproResourceSetGVK)
	rs.SetName(kapro.Name + "-workloads")
	rs.SetNamespace("flux-system")
	rs.SetAnnotations(map[string]string{
		"fluxcd.controlplane.io/reconcile":      "enabled",
		"fluxcd.controlplane.io/reconcileEvery": "5m",
	})
	rs.Object["apiVersion"] = "fluxcd.controlplane.io/v1"
	rs.Object["kind"] = "ResourceSet"
	rs.Object["spec"] = map[string]interface{}{
		"inputs":    inputs,
		"resources": resources,
	}

	return rs
}

// mergeValues resolves KaproApp defaults + matching overrides for a specific cluster.
// Returns a JSON string of the merged values, or "" if no values apply.
func (r *KaproReconciler) mergeValues(app *kaprov1alpha1.KaproApp, clusterName string, clusterLabels map[string]string) string {
	// Start with defaults.
	merged := map[string]interface{}{}
	if app.Spec.Defaults != nil && app.Spec.Defaults.Raw != nil {
		_ = json.Unmarshal(app.Spec.Defaults.Raw, &merged)
	}

	// Layer matching overrides (order matters — later overrides win).
	for _, ov := range app.Spec.Overrides {
		if !overrideMatches(ov, clusterName, clusterLabels) {
			continue
		}
		if ov.Values == nil || ov.Values.Raw == nil {
			continue
		}
		var patch map[string]interface{}
		if err := json.Unmarshal(ov.Values.Raw, &patch); err != nil {
			continue
		}
		// Scope to component if specified.
		if ov.Component != "" {
			if merged[ov.Component] == nil {
				merged[ov.Component] = map[string]interface{}{}
			}
			if compMap, ok := merged[ov.Component].(map[string]interface{}); ok {
				deepMerge(compMap, patch)
			}
		} else {
			deepMerge(merged, patch)
		}
	}

	if len(merged) == 0 {
		return ""
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return ""
	}
	return string(b)
}

// deepMerge recursively merges src into dst. Nested maps are merged, not replaced.
func deepMerge(dst, src map[string]interface{}) {
	for k, srcVal := range src {
		dstVal, exists := dst[k]
		if !exists {
			dst[k] = srcVal
			continue
		}
		srcMap, srcOk := srcVal.(map[string]interface{})
		dstMap, dstOk := dstVal.(map[string]interface{})
		if srcOk && dstOk {
			deepMerge(dstMap, srcMap)
		} else {
			dst[k] = srcVal
		}
	}
}

// overrideMatches returns true if the override applies to the given cluster.
func overrideMatches(ov kaprov1alpha1.AppOverride, clusterName string, clusterLabels map[string]string) bool {
	// Explicit cluster list takes precedence.
	if len(ov.Clusters) > 0 {
		for _, c := range ov.Clusters {
			if c == clusterName {
				return true
			}
		}
		return false
	}
	// Label selector match.
	if len(ov.Selector) > 0 {
		for k, v := range ov.Selector {
			if clusterLabels[k] != v {
				return false
			}
		}
		return true
	}
	// No selector and no clusters = matches everything.
	return true
}

// hasKubeconfigClusters returns true if any cluster in the Kapro has a kubeconfigSecret.
func hasKubeconfigClusters(kapro *kaprov1alpha1.Kapro) bool {
	for _, c := range kapro.Spec.Clusters {
		if c.KubeconfigSecret != "" {
			return true
		}
	}
	return false
}

// isNoMatchError returns true when the error indicates a missing CRD/API group.
func isNoMatchError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no matches for kind")
}

// cleanupRemovedClusters deletes MemberClusters and kubeconfig Secrets
// for clusters that were removed from the Kapro spec.
func (r *KaproReconciler) cleanupRemovedClusters(ctx context.Context, kapro *kaprov1alpha1.Kapro) {
	l := log.FromContext(ctx)

	// Build set of current cluster names.
	current := map[string]bool{}
	for _, c := range kapro.Spec.Clusters {
		current[c.Name] = true
	}

	// Delete orphaned MemberClusters.
	var mcList kaprov1alpha1.MemberClusterList
	if err := r.List(ctx, &mcList); err == nil {
		for i := range mcList.Items {
			mc := &mcList.Items[i]
			// Only clean up MemberClusters that were created by this Kapro
			// (check if it's in our inventory).
			if !current[mc.Name] && isInInventory(kapro, "MemberCluster/"+mc.Name) {
				if err := r.Delete(ctx, mc); err != nil {
					l.Error(err, "failed to delete orphaned MemberCluster", "cluster", mc.Name)
				} else {
					l.Info("deleted orphaned MemberCluster", "cluster", mc.Name)
				}
			}
		}
	}

	// Delete orphaned kubeconfig Secrets.
	var secretList corev1.SecretList
	if err := r.List(ctx, &secretList,
		client.InNamespace("flux-system"),
		client.MatchingLabels{"kapro.io/managed-by": kapro.Name},
	); err == nil {
		for i := range secretList.Items {
			s := &secretList.Items[i]
			clusterName := s.Labels["kapro.io/cluster"]
			if clusterName != "" && !current[clusterName] {
				if err := r.Delete(ctx, s); err != nil {
					l.Error(err, "failed to delete orphaned kubeconfig secret", "secret", s.Name)
				} else {
					l.Info("deleted orphaned kubeconfig secret", "secret", s.Name)
				}
			}
		}
	}
}

func isInInventory(kapro *kaprov1alpha1.Kapro, item string) bool {
	for _, inv := range kapro.Status.Inventory {
		if inv == item {
			return true
		}
	}
	return false
}

// ensureKubeconfigSecret creates or updates a kubeconfig Secret for a GCP spoke cluster.
// The kubeconfig uses gke-gcloud-auth-plugin for auth — WI tokens auto-refresh.
// For gcp-fleet: resolves cluster endpoint from Fleet membership.
// For gcp: uses the provided GCP config directly.
func (r *KaproReconciler) ensureKubeconfigSecret(ctx context.Context, kapro *kaprov1alpha1.Kapro, cluster *kaprov1alpha1.KaproCluster) (string, error) {
	if cluster.GCP == nil {
		return "", fmt.Errorf("cluster %q has provider=%s but no gcp config", cluster.Name, cluster.Provider)
	}

	p, err := provider.New(cluster.Provider, provider.Options{
		Project:  cluster.GCP.Project,
		Location: cluster.GCP.Region,
	})
	if err != nil {
		return "", fmt.Errorf("create provider for %s: %w", cluster.Name, err)
	}

	kubeconfigData, err := p.GenerateKubeConfig(ctx, cluster.Name)
	if err != nil {
		return "", fmt.Errorf("generate kubeconfig for %s: %w", cluster.Name, err)
	}

	secretName := kapro.Name + "-" + cluster.Name + "-kubeconfig"
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "flux-system",
			Labels: map[string]string{
				"kapro.io/managed-by": kapro.Name,
				"kapro.io/cluster":    cluster.Name,
			},
		},
		Data: map[string][]byte{
			"value": kubeconfigData,
		},
	}

	if err := r.Patch(ctx, secret,
		client.Apply,
		client.FieldOwner("kapro-controller"),
		client.ForceOwnership,
	); err != nil {
		return "", fmt.Errorf("apply kubeconfig secret %s: %w", secretName, err)
	}

	l := log.FromContext(ctx)
	l.Info("ensured kubeconfig secret", "secret", secretName, "cluster", cluster.Name, "provider", cluster.Provider)
	return secretName, nil
}

func (r *KaproReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Kapro{}).
		Watches(&kaprov1alpha1.KaproApp{}, handler.EnqueueRequestsFromMapFunc(r.kaproAppToKapro)).
		Complete(r)
}

// kaproAppToKapro maps a KaproApp change to the Kapro(s) that reference it.
func (r *KaproReconciler) kaproAppToKapro(ctx context.Context, obj client.Object) []reconcile.Request {
	app, ok := obj.(*kaprov1alpha1.KaproApp)
	if !ok {
		return nil
	}
	var kapros kaprov1alpha1.KaproList
	if err := r.List(ctx, &kapros); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, k := range kapros.Items {
		if k.Spec.AppRef == app.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&k),
			})
		}
	}
	return requests
}
