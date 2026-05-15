package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	bundlepkg "kapro.io/kapro/internal/bundle"
	"kapro.io/kapro/internal/provider"
)

var kaproResourceSetGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSet",
}

// KaproReconciler generates hub-side resources from a Kapro + KaproBundle spec.
// It produces (all on the hub cluster):
//   - MemberCluster CRs (one per cluster in the fleet)
//   - A Pipeline CR (from kapro.spec.pipeline)
//   - A ResourceSet (Flux Operator) with HelmRelease templates per component
//
// The ResourceSet contains per-cluster inputs with component versions and
// merged Helm values (KaproBundle defaults + overrides). Flux Operator distributes
// the rendered HelmReleases to spokes. Kapro never writes to spoke clusters.
//
// Version promotion is handled by the FluxOperatorActuator (via Release),
// which patches ResourceSet inputs — not by this controller.
type KaproReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=kaproes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=kaproes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=kaprobundles,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fluxcd.controlplane.io,resources=resourcesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch

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

	// Resolve the KaproBundle.
	var bundle kaprov1alpha1.KaproBundle
	if err := r.Get(ctx, client.ObjectKey{Name: kapro.Spec.BundleRef}, &bundle); err != nil {
		patch := client.MergeFrom(kapro.DeepCopy())
		apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: kapro.Generation,
			Reason:             "KaproBundleNotFound",
			Message:            fmt.Sprintf("KaproBundle %q not found: %v", kapro.Spec.BundleRef, err),
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

	delivery := kapro.Spec.Delivery
	if delivery.Mode == "" {
		delivery.Mode = kaprov1alpha1.DeliveryModePull
	}
	if delivery.BackendRef == "" {
		delivery.BackendRef = "flux"
	}
	spokeLocal := delivery.Mode == kaprov1alpha1.DeliveryModePull && delivery.BackendRef == "flux"

	// 2. Generate MemberClusters on the hub.
	for _, cluster := range kapro.Spec.Clusters {
		clusterDelivery := delivery
		clusterDelivery.Parameters = copyParamMap(delivery.Parameters)
		if clusterDelivery.Parameters == nil {
			clusterDelivery.Parameters = map[string]string{}
		}
		if clusterDelivery.BackendRef == "flux" {
			if clusterDelivery.Mode == kaprov1alpha1.DeliveryModePush {
				setDefaultParam(clusterDelivery.Parameters, "resourceSet", kapro.Name+"-workloads")
				setDefaultParam(clusterDelivery.Parameters, "namespace", "flux-system")
				setDefaultParam(clusterDelivery.Parameters, "inputField", "version")
				setDefaultParam(clusterDelivery.Parameters, "tenantField", "tenant")
			}
			if clusterDelivery.Mode == kaprov1alpha1.DeliveryModePull {
				setDefaultParam(clusterDelivery.Parameters, "namespace", "flux-system")
				setDefaultParam(clusterDelivery.Parameters, "ociRepository", kapro.Name+"-bundle")
			}
		}
		mc := &kaprov1alpha1.MemberCluster{
			TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "MemberCluster"},
			ObjectMeta: metav1.ObjectMeta{
				Name:   cluster.Name,
				Labels: cluster.Labels,
			},
			Spec: kaprov1alpha1.MemberClusterSpec{
				Delivery: clusterDelivery,
			},
		}
		if err := r.Patch(ctx, mc,
			client.Apply, //nolint:staticcheck // SA1019: deprecated but replacement needs larger refactor
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
		client.Apply, //nolint:staticcheck
		client.FieldOwner("kapro-controller"),
		client.ForceOwnership,
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply Pipeline: %w", err)
	}
	inventory = append(inventory, "Pipeline/"+kapro.Name+"-pipeline")

	if spokeLocal {
		// 3a. Spoke-local mode: bootstrap each spoke in parallel.
		// Bundle generation + push is done by CI via `kapro bundle generate --push`.
		// The operator only creates the spoke-side Flux resources (OCIRepository + wave Kustomizations).
		primaryVersion := bundle.Spec.Components[0].Version
		bootstrapResults := r.bootstrapSpokesParallel(ctx, &kapro, &bundle, primaryVersion)
		for clusterName, err := range bootstrapResults {
			if err != nil {
				l.Error(err, "spoke bootstrap failed (skipping)", "cluster", clusterName)
			}
		}
		inventory = append(inventory, "SpokeBootstrap/"+kapro.Name)
	} else {
		// 3b. Push mode: generate ResourceSet on the hub (Flux Operator distributes to spokes).
		rs := r.buildResourceSet(&kapro, &bundle)
		if err := r.Patch(ctx, rs,
			client.Apply, //nolint:staticcheck // SA1019: deprecated but replacement needs larger refactor
			client.FieldOwner("kapro-controller"),
			client.ForceOwnership,
		); err != nil {
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
	}

	// 4. Sync MemberCluster status from HelmRelease status (push model observability).
	// For spoke-local mode, this reads from spoke Flux resources directly.
	convergedCount := int32(0)
	for _, cluster := range kapro.Spec.Clusters {
		converged := r.syncMemberClusterStatus(ctx, &kapro, &bundle, cluster)
		if converged {
			convergedCount++
		}
	}

	// 5. Clean up orphaned resources for removed clusters.
	r.cleanupRemovedClusters(ctx, &kapro)

	// 6. Update Kapro status.
	primaryVersion := bundle.Spec.Components[0].Version
	patch := client.MergeFrom(kapro.DeepCopy())
	kapro.Status.ClusterCount = int32(len(kapro.Spec.Clusters))
	kapro.Status.ConvergedCount = convergedCount
	kapro.Status.ComponentCount = int32(len(bundle.Spec.Components))
	kapro.Status.Version = primaryVersion
	kapro.Status.ObservedGeneration = kapro.Generation
	kapro.Status.Inventory = inventory
	apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: kapro.Generation,
		Reason:             "ReconcileSuccess",
		Message:            fmt.Sprintf("%d/%d clusters converged at %s", convergedCount, len(kapro.Spec.Clusters), primaryVersion),
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
// From the KaproBundle component spec, it generates:
//   - inputs[]: one entry per cluster (tenant, kubeconfig_secret, per-component versions)
//   - resources[]: HelmRepositories + HelmReleases with dependsOn, timeout, retries, prune
//
// Flux Operator renders one set of resources per input and distributes to spokes.
func (r *KaproReconciler) buildResourceSet(kapro *kaprov1alpha1.Kapro, bundle *kaprov1alpha1.KaproBundle) *unstructured.Unstructured {
	defaults := bundle.Spec.Defaults
	if defaults == nil {
		defaults = &kaprov1alpha1.BundleDefaults{}
	}

	// Build inputs: one entry per cluster.
	primaryVersion := bundle.Spec.Components[0].Version
	inputs := make([]any, 0, len(kapro.Spec.Clusters))
	for _, cluster := range kapro.Spec.Clusters {
		input := map[string]any{
			"tenant":  cluster.Name,
			"version": primaryVersion,
		}
		if cluster.KubeconfigSecret != "" {
			input["kubeconfig_secret"] = cluster.KubeconfigSecret
		}
		mergedValues := r.mergeValues(bundle, cluster.Name, cluster.Labels)
		if mergedValues != "" {
			input["values_override"] = mergedValues
		}
		inputs = append(inputs, input)
	}

	resources := make([]any, 0, len(bundle.Spec.Components)+len(bundle.Spec.Registries))

	// Build HelmRepositories from registries.
	for _, reg := range bundle.Spec.Registries {
		repoSpec := map[string]any{
			"interval": resolveDefault(reg.Interval, "5m"),
			"url":      reg.URL,
		}
		if reg.Provider != "" && reg.Provider != "generic" {
			repoSpec["provider"] = reg.Provider
		}
		if reg.Type == "oci" || strings.HasPrefix(reg.URL, "oci://") {
			repoSpec["type"] = "oci"
		}
		resources = append(resources, map[string]any{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       "HelmRepository",
			"metadata": map[string]any{
				"name":      reg.Name,
				"namespace": "flux-system",
				"labels":    map[string]any{"kapro.io/managed-by": kapro.Name},
			},
			"spec": repoSpec,
		})
	}

	// Fallback: if no registries defined, use kapro.spec.registry (backward compat).
	if len(bundle.Spec.Registries) == 0 && kapro.Spec.Registry.URL != "" {
		resources = append(resources, map[string]any{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       "HelmRepository",
			"metadata": map[string]any{
				"name":      kapro.Name + "-registry",
				"namespace": "flux-system",
			},
			"spec": map[string]any{
				"type":     "oci",
				"interval": "5m",
				"url":      kapro.Spec.Registry.URL,
				"provider": kapro.Spec.Registry.Provider,
			},
		})
	}

	// Build HelmReleases — one per component.
	for _, comp := range bundle.Spec.Components {
		resources = append(resources, r.buildHelmRelease(kapro, defaults, comp))
	}

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
	rs.Object["spec"] = map[string]any{
		"inputs":    inputs,
		"resources": resources,
	}
	return rs
}

// buildHelmRelease generates one HelmRelease from a component spec + defaults.
// Output matches the exact structure from the integration monorepo.
func (r *KaproReconciler) buildHelmRelease(kapro *kaprov1alpha1.Kapro, defaults *kaprov1alpha1.BundleDefaults, comp kaprov1alpha1.BundleComponent) map[string]any {
	// Resolve fields: component overrides defaults.
	chartName := comp.Name
	if comp.ChartName != "" {
		chartName = comp.ChartName
	}
	repo := resolveDefault(comp.Repo, defaults.Repo)
	if repo == "" {
		repo = kapro.Name + "-registry"
	}
	targetNS := resolveDefault(comp.TargetNamespace, defaults.TargetNamespace)
	if targetNS == "" {
		targetNS = "flux-system"
	}
	timeout := resolveDefault(comp.Timeout, defaults.Timeout)
	if timeout == "" {
		timeout = "10m"
	}
	retries := defaults.Retries
	if comp.Retries != nil {
		retries = *comp.Retries
	}
	if retries == 0 {
		retries = 3
	}

	// Build the HelmRelease spec.
	hrSpec := map[string]any{
		"interval": "5m",
		"chart": map[string]any{
			"spec": map[string]any{
				"chart":   chartName,
				"version": "<< inputs.version >>",
				"sourceRef": map[string]any{
					"kind": "HelmRepository",
					"name": repo,
				},
			},
		},
		"targetNamespace": targetNS,
		"releaseName":     comp.Name,
		"install": map[string]any{
			"timeout":     timeout,
			"remediation": map[string]any{"retries": retries},
		},
		"upgrade": map[string]any{
			"timeout":     timeout,
			"remediation": map[string]any{"retries": retries},
		},
	}

	// CRD install policy.
	if comp.CRDs == "Create" || comp.CRDs == "CreateReplace" {
		hrSpec["install"].(map[string]any)["crds"] = comp.CRDs
		if comp.CRDs == "Create" {
			hrSpec["upgrade"].(map[string]any)["crds"] = "CreateReplace"
		} else {
			hrSpec["upgrade"].(map[string]any)["crds"] = comp.CRDs
		}
	}

	// Merge values: defaults.values deep-merged with component.values.
	mergedValues := r.mergeComponentValues(defaults, comp)
	if len(mergedValues) > 0 {
		hrSpec["values"] = mergedValues
	}

	// ValuesFrom: component replaces defaults if set.
	valuesFrom := r.resolveValuesFrom(defaults, comp)
	if len(valuesFrom) > 0 {
		hrSpec["valuesFrom"] = valuesFrom
	}

	// DependsOn: component-level dependencies (HelmRelease within same wave).
	if len(comp.DependsOn) > 0 {
		deps := make([]any, 0, len(comp.DependsOn))
		for _, d := range comp.DependsOn {
			deps = append(deps, map[string]any{"name": d + "-<< inputs.tenant >>"})
		}
		hrSpec["dependsOn"] = deps
	}

	// Suspend.
	if comp.Suspend {
		hrSpec["suspend"] = true
	}

	// KubeConfig for cross-cluster delivery (hub→spoke push).
	if hasKubeconfigClusters(kapro) {
		hrSpec["kubeConfig"] = map[string]any{
			"secretRef": map[string]any{
				"name": "<< inputs.kubeconfig_secret >>",
			},
		}
	}

	return map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      comp.Name + "-<< inputs.tenant >>",
			"namespace": "flux-system",
			"labels": map[string]any{
				"kapro.io/managed-by": kapro.Name,
				"kapro.io/wave":       fmt.Sprintf("%d", comp.Wave),
			},
		},
		"spec": hrSpec,
	}
}

// mergeComponentValues deep-merges defaults.values + component.values.
func (r *KaproReconciler) mergeComponentValues(defaults *kaprov1alpha1.BundleDefaults, comp kaprov1alpha1.BundleComponent) map[string]any {
	merged := map[string]any{}
	if defaults.Values != nil && defaults.Values.Raw != nil {
		_ = json.Unmarshal(defaults.Values.Raw, &merged)
	}
	if comp.Values != nil && comp.Values.Raw != nil {
		var compVals map[string]any
		if err := json.Unmarshal(comp.Values.Raw, &compVals); err == nil {
			deepMerge(merged, compVals)
		}
	}
	return merged
}

// resolveValuesFrom returns component's valuesFrom if set, otherwise defaults'.
func (r *KaproReconciler) resolveValuesFrom(defaults *kaprov1alpha1.BundleDefaults, comp kaprov1alpha1.BundleComponent) []any {
	refs := defaults.ValuesFrom
	if len(comp.ValuesFrom) > 0 {
		refs = comp.ValuesFrom
	}
	if len(refs) == 0 {
		return nil
	}
	result := make([]any, 0, len(refs))
	for _, vf := range refs {
		entry := map[string]any{
			"kind": resolveDefault(vf.Kind, "ConfigMap"),
			"name": vf.Name,
		}
		if vf.ValuesKey != "" {
			entry["valuesKey"] = vf.ValuesKey
		}
		if vf.Optional {
			entry["optional"] = true
		}
		result = append(result, entry)
	}
	return result
}

func resolveDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// mergeValues resolves KaproBundle defaults + matching overrides for a specific cluster.
// Returns a JSON string of the merged values, or "" if no values apply.
func (r *KaproReconciler) mergeValues(bundle *kaprov1alpha1.KaproBundle, clusterName string, clusterLabels map[string]string) string {
	// Start with defaults.
	merged := map[string]interface{}{}
	if bundle.Spec.Defaults != nil && bundle.Spec.Defaults.Values != nil && bundle.Spec.Defaults.Values.Raw != nil {
		_ = json.Unmarshal(bundle.Spec.Defaults.Values.Raw, &merged)
	}

	// Layer matching overrides (order matters — later overrides win).
	for _, ov := range bundle.Spec.Overrides {
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
func overrideMatches(ov kaprov1alpha1.BundleOverride, clusterName string, clusterLabels map[string]string) bool {
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

// isSpokeLocalMode returns true if this Kapro still uses the built-in Flux
// spoke bootstrap path. Non-Flux backends are handled by their adapters/agents.
func isSpokeLocalMode(kapro *kaprov1alpha1.Kapro) bool {
	delivery := kapro.Spec.Delivery
	if delivery.Mode == "" {
		delivery.Mode = kaprov1alpha1.DeliveryModePull
	}
	if delivery.BackendRef == "" {
		delivery.BackendRef = "flux"
	}
	return delivery.Mode == kaprov1alpha1.DeliveryModePull && delivery.BackendRef == "flux"
}

func copyParamMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func setDefaultParam(params map[string]string, key, value string) {
	if params[key] == "" {
		params[key] = value
	}
}

const maxConcurrentBootstraps = 10

// bootstrapSpokesParallel bootstraps all spokes concurrently with bounded parallelism.
// Each spoke is independent — a failing spoke doesn't block others.
// Returns a map of cluster name → error (nil = success).
func (r *KaproReconciler) bootstrapSpokesParallel(ctx context.Context, kapro *kaprov1alpha1.Kapro, bundle *kaprov1alpha1.KaproBundle, version string) map[string]error {
	l := log.FromContext(ctx)
	results := make(map[string]error, len(kapro.Spec.Clusters))
	var mu sync.Mutex

	sem := make(chan struct{}, maxConcurrentBootstraps)
	var wg sync.WaitGroup

	for _, cluster := range kapro.Spec.Clusters {
		cluster := cluster // capture loop variable

		// Version-change detection: skip if spoke already has this version.
		if r.spokeAlreadyBootstrapped(ctx, cluster.Name, version) {
			l.V(1).Info("spoke already at target version, skipping bootstrap",
				"cluster", cluster.Name, "version", version)
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			err := r.bootstrapSpoke(ctx, kapro, bundle, cluster, version)
			mu.Lock()
			results[cluster.Name] = err
			mu.Unlock()
		}()
	}

	wg.Wait()
	return results
}

// spokeAlreadyBootstrapped checks if the spoke's MemberCluster already reports
// the target version. Avoids redundant bootstrap calls on every reconcile.
func (r *KaproReconciler) spokeAlreadyBootstrapped(ctx context.Context, clusterName, targetVersion string) bool {
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, client.ObjectKey{Name: clusterName}, &mc); err != nil {
		return false
	}
	return mc.Status.Version == targetVersion && mc.Status.Phase == kaprov1alpha1.ClusterPhaseConverged
}

// bootstrapSpoke connects to a spoke cluster via its kubeconfig secret and
// applies the OCIRepository + wave Kustomizations directly. This is called
// once per reconcile — Kubernetes server-side apply is idempotent so repeated
// calls are safe and only patch if something changed.
//
// We apply directly to spoke instead of using ResourceSet because OCIRepository
// has no kubeConfig field — it can't be created remotely via Flux.
func (r *KaproReconciler) bootstrapSpoke(ctx context.Context, kapro *kaprov1alpha1.Kapro, bundle *kaprov1alpha1.KaproBundle, cluster kaprov1alpha1.KaproCluster, version string) error {
	l := log.FromContext(ctx)

	if cluster.KubeconfigSecret == "" {
		return fmt.Errorf("cluster %s has no kubeconfig secret", cluster.Name)
	}

	// Read kubeconfig secret from hub.
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{
		Name:      cluster.KubeconfigSecret,
		Namespace: "flux-system",
	}, &secret); err != nil {
		return fmt.Errorf("get kubeconfig secret %s: %w", cluster.KubeconfigSecret, err)
	}

	kubeconfigData := secret.Data["value"]
	if len(kubeconfigData) == 0 {
		return fmt.Errorf("kubeconfig secret %s has no 'value' key", cluster.KubeconfigSecret)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return fmt.Errorf("parse kubeconfig for %s: %w", cluster.Name, err)
	}

	spokeClient, err := client.New(restConfig, client.Options{})
	if err != nil {
		return fmt.Errorf("create spoke client for %s: %w", cluster.Name, err)
	}

	bundleURL := kapro.Spec.Registry.URL + "/" + kapro.Name + "-bundle"
	ociProvider := resolveDefault(kapro.Spec.Registry.Provider, "generic")

	// 1. Apply OCIRepository on spoke.
	ociRepo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "OCIRepository",
		"metadata": map[string]any{
			"name":      kapro.Name + "-bundle",
			"namespace": "flux-system",
			"labels":    map[string]any{"kapro.io/managed-by": kapro.Name},
		},
		"spec": map[string]any{
			"interval": "5m",
			"url":      bundleURL,
			"provider": ociProvider,
			"ref":      map[string]any{"tag": version},
		},
	}}
	if err := spokeClient.Patch(ctx, ociRepo,
		client.Apply, //nolint:staticcheck
		client.FieldOwner("kapro-controller"),
		client.ForceOwnership,
	); err != nil {
		return fmt.Errorf("apply OCIRepository on spoke %s: %w", cluster.Name, err)
	}

	// 2. Apply wave Kustomizations on spoke.
	waveKusts := bundlepkg.WaveKustomizations(kapro.Name, bundle)
	for _, wk := range waveKusts {
		obj := &unstructured.Unstructured{Object: wk}
		if err := spokeClient.Patch(ctx, obj,
			client.Apply, //nolint:staticcheck // SA1019: deprecated but replacement needs larger refactor
			client.FieldOwner("kapro-controller"),
			client.ForceOwnership,
		); err != nil {
			return fmt.Errorf("apply wave Kustomization %s on spoke %s: %w",
				obj.GetName(), cluster.Name, err)
		}
	}

	l.Info("bootstrapped spoke", "cluster", cluster.Name,
		"ociRepository", kapro.Name+"-bundle", "waves", len(waveKusts), "version", version)
	return nil
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

// syncMemberClusterStatus reads HelmRelease status and writes it to the
// MemberCluster status. For push mode, reads from hub. For spoke-local mode,
// connects to spoke and reads directly.
func (r *KaproReconciler) syncMemberClusterStatus(ctx context.Context, kapro *kaprov1alpha1.Kapro, bundle *kaprov1alpha1.KaproBundle, cluster kaprov1alpha1.KaproCluster) bool {
	l := log.FromContext(ctx)

	// Read the MemberCluster.
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, client.ObjectKey{Name: cluster.Name}, &mc); err != nil {
		return false
	}

	// Determine which client to use for reading HelmRelease status.
	hrClient := r.Client               // default: hub (push mode)
	hrNameSuffix := "-" + cluster.Name // push mode: hello-infra-kapro-spoke

	if isSpokeLocalMode(kapro) {
		// Spoke-local: connect to spoke and read HelmReleases there.
		if cluster.KubeconfigSecret == "" {
			l.Info("no kubeconfig secret for spoke, skipping status sync", "cluster", cluster.Name)
			return false
		}
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Name: cluster.KubeconfigSecret, Namespace: "flux-system"}, &secret); err != nil {
			l.Error(err, "failed to read kubeconfig secret", "cluster", cluster.Name)
			return false
		}
		restConfig, err := clientcmd.RESTConfigFromKubeConfig(secret.Data["value"])
		if err != nil {
			l.Error(err, "failed to parse kubeconfig", "cluster", cluster.Name)
			return false
		}
		spokeClient, err := client.New(restConfig, client.Options{})
		if err != nil {
			l.Error(err, "failed to create spoke client", "cluster", cluster.Name)
			return false
		}
		hrClient = spokeClient
		hrNameSuffix = "" // spoke-local: just "hello-infra", no cluster suffix
	}

	// Read HelmRelease status for each component.
	allReady := true
	versions := map[string]string{}
	for _, comp := range bundle.Spec.Components {
		hrName := comp.Name + hrNameSuffix
		hr := &unstructured.Unstructured{}
		hr.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
		})
		if err := hrClient.Get(ctx, client.ObjectKey{Name: hrName, Namespace: "flux-system"}, hr); err != nil {
			allReady = false
			continue
		}

		// Check Ready condition.
		status, _ := hr.Object["status"].(map[string]any)
		ready := false
		if status != nil {
			conditions, _ := status["conditions"].([]any)
			for _, c := range conditions {
				cm, _ := c.(map[string]any)
				if cm != nil && fmt.Sprintf("%v", cm["type"]) == "Ready" && fmt.Sprintf("%v", cm["status"]) == "True" {
					ready = true
					break
				}
			}
		}

		if ready {
			// Read the actual deployed version from the HelmRelease status.
			deployedVersion := comp.Version
			if status != nil {
				if lar, ok := status["lastAppliedRevision"].(string); ok && lar != "" {
					deployedVersion = lar
				}
				// Also try lastAttemptedRevision for the chart version.
				if latv, ok := status["lastAttemptedRevision"].(string); ok && latv != "" {
					deployedVersion = latv
				}
			}
			versions["default"] = deployedVersion
			versions[comp.Name] = deployedVersion
		} else {
			allReady = false
		}
	}

	// Determine phase.
	phase := kaprov1alpha1.ClusterPhaseConverging
	if allReady {
		phase = kaprov1alpha1.ClusterPhaseConverged
	}

	// For spoke-local: version is the OCIRepository tag (bundle version), not chart version.
	if isSpokeLocalMode(kapro) && allReady && hrClient != r.Client {
		ociRepo := &unstructured.Unstructured{}
		ociRepo.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "OCIRepository",
		})
		ociRepoName := kapro.Name + "-bundle"
		if err := hrClient.Get(ctx, client.ObjectKey{Name: ociRepoName, Namespace: "flux-system"}, ociRepo); err == nil {
			if spec, ok := ociRepo.Object["spec"].(map[string]any); ok {
				if ref, ok := spec["ref"].(map[string]any); ok {
					if tag, ok := ref["tag"].(string); ok && tag != "" {
						versions["default"] = tag
						for k := range versions {
							versions[k] = tag
						}
					}
				}
			}
		}
	}

	// Patch MemberCluster status.
	mcPatch := client.MergeFrom(mc.DeepCopy())
	mc.Status.Phase = phase
	mc.Status.CurrentVersions = versions
	mc.Status.Version = versions["default"]
	if mc.Status.Version == "" {
		mc.Status.Version = bundle.Spec.Components[0].Version
	}
	mc.Status.Provider = cluster.Provider
	if isSpokeLocalMode(kapro) {
		mc.Status.DeliverySystem = "spoke"
	} else {
		mc.Status.DeliverySystem = "flux-operator"
	}
	mc.Status.Health = kaprov1alpha1.ClusterHealth{
		AllWorkloadsReady: allReady,
		ReadyWorkloads:    len(versions),
		TotalWorkloads:    len(bundle.Spec.Components),
	}
	mc.Status.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)

	readyStatus := metav1.ConditionFalse
	reason := "Converging"
	if allReady {
		readyStatus = metav1.ConditionTrue
		reason = "AllHelmReleasesReady"
	}
	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             readyStatus,
		ObservedGeneration: mc.Generation,
		Reason:             reason,
		Message:            fmt.Sprintf("%d/%d components ready", len(versions), len(bundle.Spec.Components)),
	})

	if err := r.Status().Patch(ctx, &mc, mcPatch); err != nil {
		l.Error(err, "failed to patch MemberCluster status", "cluster", cluster.Name)
	}

	return allReady
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
		client.Apply, //nolint:staticcheck
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
		Watches(&kaprov1alpha1.KaproBundle{}, handler.EnqueueRequestsFromMapFunc(r.kaproBundleToKapro)).
		Complete(r)
}

// kaproBundleToKapro maps a KaproBundle change to the Kapro(s) that reference it.
func (r *KaproReconciler) kaproBundleToKapro(ctx context.Context, obj client.Object) []reconcile.Request {
	bundle, ok := obj.(*kaprov1alpha1.KaproBundle)
	if !ok {
		return nil
	}
	var kapros kaprov1alpha1.KaproList
	if err := r.List(ctx, &kapros); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, k := range kapros.Items {
		if k.Spec.BundleRef == bundle.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&k),
			})
		}
	}
	return requests
}
