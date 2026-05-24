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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	bundlepkg "kapro.io/kapro/internal/bundle"
	"kapro.io/kapro/internal/provider"
	"kapro.io/kapro/internal/webhook/admission"
)

var kaproResourceSetGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSet",
}

// FleetReconciler generates hub-side resources from a Kapro source spec.
// It produces (all on the hub cluster):
//   - Cluster CRs (one per cluster in the fleet)
//   - A Plan CR (from Fleet.spec.plan)
//   - A ResourceSet (Flux Operator) with HelmRelease templates per unit
//
// The ResourceSet contains per-cluster inputs with unit versions and
// merged Helm values (PromotionSource defaults + overrides). Flux Operator distributes
// the rendered HelmReleases to spokes. Kapro never writes to spoke clusters.
//
// Version promotion is handled by the FluxOperatorActuator (via PromotionRun),
// which patches ResourceSet inputs — not by this controller.
type FleetReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=fleets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=fleets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=sources,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=plans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fluxcd.controlplane.io,resources=resourcesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch

func (r *FleetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var kapro kaprov1alpha1.Fleet
	if err := r.Get(ctx, req.NamespacedName, &kapro); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if kapro.Spec.Suspended {
		l.Info("Kapro is suspended, skipping")
		return ctrl.Result{}, nil
	}

	l.Info("reconciling Kapro", "name", kapro.Name)

	source, ok, err := r.resolvePromotionSource(ctx, &kapro)
	if err != nil {
		patch := client.MergeFrom(kapro.DeepCopy())
		apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: kapro.Generation,
			Reason:             "PromotionSourceNotFound",
			Message:            fmt.Sprintf("PromotionSource %q not found: %v", kapro.Spec.SourceRef, err),
		})
		_ = r.Status().Patch(ctx, &kapro, patch)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if !ok {
		patch := client.MergeFrom(kapro.DeepCopy())
		apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: kapro.Generation,
			Reason:             "SourceNotConfigured",
			Message:            "set spec.source for the single-object path or spec.sourceRef for a separate PromotionSource",
		})
		_ = r.Status().Patch(ctx, &kapro, patch)
		return ctrl.Result{}, nil
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
	if delivery.SubstrateRef == "" {
		delivery.SubstrateRef = "flux"
	}
	deliveryPath := resolveFleetDeliveryPath(delivery)

	// 2. Generate Clusters on the hub.
	for _, cluster := range kapro.Spec.Clusters {
		clusterDelivery := delivery
		clusterDelivery.Parameters = copyParamMap(delivery.Parameters)
		if clusterDelivery.Parameters == nil {
			clusterDelivery.Parameters = map[string]string{}
		}
		if clusterDelivery.SubstrateRef == "flux" {
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
		mc := &kaprov1alpha1.Cluster{
			TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Cluster"},
			ObjectMeta: metav1.ObjectMeta{
				Name:   cluster.Name,
				Labels: cluster.Labels,
			},
			Spec: kaprov1alpha1.ClusterSpec{
				Delivery: clusterDelivery,
			},
		}
		if err := r.Patch(ctx, mc,
			client.Apply, //nolint:staticcheck // SA1019: deprecated but replacement needs larger refactor
			client.FieldOwner("kapro-controller"),
			client.ForceOwnership,
		); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply Cluster %s: %w", cluster.Name, err)
		}
		inventory = append(inventory, "Cluster/"+cluster.Name)
	}

	// 2. Generate Plan on the hub.
	promotionplan := r.buildPromotionPlan(&kapro)
	if err := r.Patch(ctx, promotionplan,
		client.Apply, //nolint:staticcheck
		client.FieldOwner("kapro-controller"),
		client.ForceOwnership,
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply Plan: %w", err)
	}
	inventory = append(inventory, "Plan/"+InlinePromotionPlanName(kapro.Name))

	switch deliveryPath {
	case fleetDeliveryPathFluxSpoke:
		// 3a. Spoke-local mode: bootstrap each spoke in parallel.
		// Source packaging + push is done by CI via `kapro source package --push`.
		// The operator only creates the spoke-side Flux resources (OCIRepository + wave Kustomizations).
		primaryVersion := promotionSourcePrimaryVersion(source)
		if primaryVersion != "" {
			bootstrapResults := r.bootstrapSpokesParallel(ctx, &kapro, source, primaryVersion)
			for clusterName, err := range bootstrapResults {
				if err != nil {
					l.Error(err, "spoke bootstrap failed (skipping)", "cluster", clusterName)
				}
			}
		} else {
			l.Info("skipping spoke bootstrap: PromotionSource has no packaged unit version", "source", source.Name)
		}
		inventory = append(inventory, "SpokeBootstrap/"+kapro.Name)
	case fleetDeliveryPathFluxOperator:
		// 3b. Push mode: generate ResourceSet on the hub (Flux Operator distributes to spokes).
		rs := r.buildResourceSet(&kapro, source)
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
	case fleetDeliveryPathNative:
		inventory = append(inventory, "Substrate/"+delivery.SubstrateRef)
	}

	// 4. Sync Cluster status from HelmRelease status (push model observability).
	// For spoke-local mode, this reads from spoke Flux resources directly.
	convergedCount := int32(0)
	if deliveryPath == fleetDeliveryPathFluxSpoke || deliveryPath == fleetDeliveryPathFluxOperator {
		for _, cluster := range kapro.Spec.Clusters {
			converged := r.syncFleetClusterStatus(ctx, &kapro, source, cluster)
			if converged {
				convergedCount++
			}
		}
	}

	// 5. Clean up orphaned resources for removed clusters.
	r.cleanupRemovedClusters(ctx, &kapro)

	// 6. Update Kapro status.
	primaryVersion := promotionSourcePrimaryVersion(source)
	patch := client.MergeFrom(kapro.DeepCopy())
	kapro.Status.ClusterCount = int32(len(kapro.Spec.Clusters))
	kapro.Status.ConvergedCount = convergedCount
	kapro.Status.UnitCount = int32(len(source.Spec.Units))
	kapro.Status.Version = primaryVersion
	kapro.Status.ObservedGeneration = kapro.Generation
	kapro.Status.Inventory = inventory
	apimeta.SetStatusCondition(&kapro.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: kapro.Generation,
		Reason:             "ReconcileSuccess",
		Message:            kaproReadyMessage(deliveryPath, convergedCount, len(kapro.Spec.Clusters), primaryVersion),
	})
	if err := r.Status().Patch(ctx, &kapro, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Kapro status: %w", err)
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *FleetReconciler) resolvePromotionSource(ctx context.Context, kapro *kaprov1alpha1.Fleet) (*kaprov1alpha1.Source, bool, error) {
	if kapro.Spec.SourceRef != "" {
		var source kaprov1alpha1.Source
		if err := r.Get(ctx, client.ObjectKey{Name: kapro.Spec.SourceRef}, &source); err != nil {
			return nil, false, err
		}
		return &source, true, nil
	}
	if kapro.Spec.Source == nil {
		return nil, false, nil
	}
	return &kaprov1alpha1.Source{
		ObjectMeta: metav1.ObjectMeta{Name: kapro.Name},
		Spec:       *kapro.Spec.Source,
	}, true, nil
}

func (r *FleetReconciler) buildPromotionPlan(kapro *kaprov1alpha1.Fleet) *kaprov1alpha1.Plan {
	stages := make([]kaprov1alpha1.Stage, 0, len(kapro.Spec.Plan.Stages))
	for _, s := range kapro.Spec.Plan.Stages {
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
	// Propagate the parent Fleet's tenancy + standard labels to the
	// generated Plan. Without this the admission webhook
	// rejects the controller's own write with a missing-team error,
	// because the generated plan inherits no labels from anywhere.
	labels := map[string]string{}
	if team, ok := kapro.Labels[admission.LabelKaproTeam]; ok && team != "" {
		labels[admission.LabelKaproTeam] = team
	}
	labels["app.kubernetes.io/managed-by"] = "kapro-operator"
	labels["kapro.io/owned-by-kapro"] = kapro.Name

	return &kaprov1alpha1.Plan{
		TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Plan"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   InlinePromotionPlanName(kapro.Name),
			Labels: labels,
		},
		Spec: kaprov1alpha1.PlanSpec{Stages: stages},
	}
}

// InlinePromotionPlanName returns the deterministic name of the Plan
// CR generated by the Fleet reconciler from Fleet.spec.plan.
// PromotionController references the same name when stamping inline plan refs
// on a PromotionRun, so any change here must update both call sites.
func InlinePromotionPlanName(kaproName string) string {
	return kaproName + "-promotionplan"
}

// buildResourceSet creates a Flux Operator ResourceSet on the hub.
// From the PromotionSource unit spec, it generates:
//   - inputs[]: one entry per cluster (tenant, kubeconfig_secret, per-unit versions)
//   - resources[]: HelmRepositories + HelmReleases with dependsOn, timeout, retries, prune
//
// Flux Operator renders one set of resources per input and distributes to spokes.
func (r *FleetReconciler) buildResourceSet(kapro *kaprov1alpha1.Fleet, source *kaprov1alpha1.Source) *unstructured.Unstructured {
	defaults := source.Spec.Defaults
	if defaults == nil {
		defaults = &kaprov1alpha1.SourceDefaults{}
	}

	// Build inputs: one entry per cluster.
	primaryVersion := promotionSourcePrimaryVersion(source)
	inputs := make([]any, 0, len(kapro.Spec.Clusters))
	for _, cluster := range kapro.Spec.Clusters {
		input := map[string]any{
			"tenant":  cluster.Name,
			"version": primaryVersion,
		}
		if cluster.KubeconfigSecret != "" {
			input["kubeconfig_secret"] = cluster.KubeconfigSecret
		}
		mergedValues := r.mergeValues(source, cluster.Name, cluster.Labels)
		if mergedValues != "" {
			input["values_override"] = mergedValues
		}
		inputs = append(inputs, input)
	}

	resources := make([]any, 0, len(source.Spec.Units)+len(source.Spec.Registries))

	// Build HelmRepositories from registries.
	for _, reg := range source.Spec.Registries {
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
	if len(source.Spec.Registries) == 0 && kapro.Spec.Registry.URL != "" {
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

	// Build HelmReleases — one per unit.
	for _, comp := range source.Spec.Units {
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

// buildHelmRelease generates one HelmRelease from a unit spec + defaults.
// Output matches the exact structure from the integration monorepo.
func (r *FleetReconciler) buildHelmRelease(kapro *kaprov1alpha1.Fleet, defaults *kaprov1alpha1.SourceDefaults, comp kaprov1alpha1.Unit) map[string]any {
	// Resolve fields: unit overrides defaults.
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
		"targetNamespace":  targetNS,
		"promotionrunName": comp.Name,
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

	// Merge values: defaults.values deep-merged with unit.values.
	mergedValues := r.mergeUnitValues(defaults, comp)
	if len(mergedValues) > 0 {
		hrSpec["values"] = mergedValues
	}

	// ValuesFrom: unit replaces defaults if set.
	valuesFrom := r.resolveValuesFrom(defaults, comp)
	if len(valuesFrom) > 0 {
		hrSpec["valuesFrom"] = valuesFrom
	}

	// DependsOn: unit-level dependencies (HelmRelease within same wave).
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

// mergeUnitValues deep-merges defaults.values + unit.values.
func (r *FleetReconciler) mergeUnitValues(defaults *kaprov1alpha1.SourceDefaults, comp kaprov1alpha1.Unit) map[string]any {
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

// resolveValuesFrom returns unit's valuesFrom if set, otherwise defaults'.
func (r *FleetReconciler) resolveValuesFrom(defaults *kaprov1alpha1.SourceDefaults, comp kaprov1alpha1.Unit) []any {
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

func promotionSourcePrimaryVersion(source *kaprov1alpha1.Source) string {
	if source == nil {
		return ""
	}
	for _, unit := range source.Spec.Units {
		if unit.Version != "" {
			return unit.Version
		}
	}
	return ""
}

func kaproReadyMessage(path fleetDeliveryPath, converged int32, total int, version string) string {
	if path == fleetDeliveryPathNative {
		return fmt.Sprintf("%d/%d clusters configured; promotion convergence is tracked on Targets", total, total)
	}
	if version == "" {
		return fmt.Sprintf("%d/%d clusters converged", converged, total)
	}
	return fmt.Sprintf("%d/%d clusters converged at %s", converged, total, version)
}

type fleetDeliveryPath string

const (
	fleetDeliveryPathNative       fleetDeliveryPath = "native"
	fleetDeliveryPathFluxSpoke    fleetDeliveryPath = "flux-spoke"
	fleetDeliveryPathFluxOperator fleetDeliveryPath = "flux-operator"
)

func fleetDeliveryPathForFleet(kapro *kaprov1alpha1.Fleet) fleetDeliveryPath {
	if kapro == nil {
		return fleetDeliveryPathFluxSpoke
	}
	delivery := kapro.Spec.Delivery
	if delivery.Mode == "" {
		delivery.Mode = kaprov1alpha1.DeliveryModePull
	}
	if delivery.SubstrateRef == "" {
		delivery.SubstrateRef = "flux"
	}
	return resolveFleetDeliveryPath(delivery)
}

func resolveFleetDeliveryPath(delivery kaprov1alpha1.DeliverySpec) fleetDeliveryPath {
	if delivery.Mode == "" {
		delivery.Mode = kaprov1alpha1.DeliveryModePull
	}
	if delivery.SubstrateRef == "" {
		delivery.SubstrateRef = "flux"
	}
	if delivery.SubstrateRef != "flux" {
		return fleetDeliveryPathNative
	}
	if delivery.Mode == kaprov1alpha1.DeliveryModePull {
		return fleetDeliveryPathFluxSpoke
	}
	return fleetDeliveryPathFluxOperator
}

// mergeValues resolves PromotionSource defaults + matching overrides for a specific cluster.
// Returns a JSON string of the merged values, or "" if no values apply.
func (r *FleetReconciler) mergeValues(source *kaprov1alpha1.Source, clusterName string, clusterLabels map[string]string) string {
	// Start with defaults.
	merged := map[string]interface{}{}
	if source.Spec.Defaults != nil && source.Spec.Defaults.Values != nil && source.Spec.Defaults.Values.Raw != nil {
		_ = json.Unmarshal(source.Spec.Defaults.Values.Raw, &merged)
	}

	// Layer matching overrides (order matters — later overrides win).
	for _, ov := range source.Spec.Overrides {
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
		// Scope to unit if specified.
		if ov.Unit != "" {
			if merged[ov.Unit] == nil {
				merged[ov.Unit] = map[string]interface{}{}
			}
			if compMap, ok := merged[ov.Unit].(map[string]interface{}); ok {
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
func overrideMatches(ov kaprov1alpha1.SourceOverride, clusterName string, clusterLabels map[string]string) bool {
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

// isSpokeLocalMode returns true if this Fleet still uses the built-in Flux
// spoke bootstrap path. Non-Flux substrates are handled by their adapters.
func isSpokeLocalMode(kapro *kaprov1alpha1.Fleet) bool {
	return fleetDeliveryPathForFleet(kapro) == fleetDeliveryPathFluxSpoke
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
func (r *FleetReconciler) bootstrapSpokesParallel(ctx context.Context, kapro *kaprov1alpha1.Fleet, source *kaprov1alpha1.Source, version string) map[string]error {
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
			defer func() { <-sem }() // promotionrun

			err := r.bootstrapSpoke(ctx, kapro, source, cluster, version)
			mu.Lock()
			results[cluster.Name] = err
			mu.Unlock()
		}()
	}

	wg.Wait()
	return results
}

// spokeAlreadyBootstrapped checks if the spoke's Cluster already reports
// the target version. Avoids redundant bootstrap calls on every reconcile.
func (r *FleetReconciler) spokeAlreadyBootstrapped(ctx context.Context, clusterName, targetVersion string) bool {
	var mc kaprov1alpha1.Cluster
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
func (r *FleetReconciler) bootstrapSpoke(ctx context.Context, kapro *kaprov1alpha1.Fleet, source *kaprov1alpha1.Source, cluster kaprov1alpha1.ClusterRef, version string) error {
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
	waveKusts := bundlepkg.WaveKustomizations(kapro.Name, source)
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
func hasKubeconfigClusters(kapro *kaprov1alpha1.Fleet) bool {
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

// syncFleetClusterStatus reads HelmRelease status and writes it to the
// Cluster status. For push mode, reads from hub. For spoke-local mode,
// connects to spoke and reads directly.
func (r *FleetReconciler) syncFleetClusterStatus(ctx context.Context, kapro *kaprov1alpha1.Fleet, source *kaprov1alpha1.Source, cluster kaprov1alpha1.ClusterRef) bool {
	l := log.FromContext(ctx)

	// Read the Cluster.
	var mc kaprov1alpha1.Cluster
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

	// Read HelmRelease status for each unit.
	allReady := true
	versions := map[string]string{}
	for _, comp := range source.Spec.Units {
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

	// Determine phase. Reachability wins: when the heartbeat reconciler has
	// set Ready=False reason=Unreachable, surface that as Phase=Unreachable
	// regardless of convergence state. We can't trust convergence numbers
	// from a cluster we can't reach. The heartbeat reconciler is the sole
	// writer of conditions[Ready]; this is the only place that reads it to
	// influence Phase. See fleetcluster_heartbeat_controller.go for the
	// state machine that produces the condition.
	phase := kaprov1alpha1.ClusterPhaseConverging
	if allReady {
		phase = kaprov1alpha1.ClusterPhaseConverged
	}
	if ready := apimeta.FindStatusCondition(mc.Status.Conditions, kaprov1alpha1.ConditionTypeReady); ready != nil &&
		ready.Status == metav1.ConditionFalse && ready.Reason == kaprov1alpha1.ReasonUnreachable {
		phase = kaprov1alpha1.ClusterPhaseUnreachable
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

	// Patch Cluster status.
	mcPatch := client.MergeFrom(mc.DeepCopy())
	mc.Status.Phase = phase
	mc.Status.CurrentVersions = versions
	mc.Status.Version = versions["default"]
	if mc.Status.Version == "" {
		mc.Status.Version = promotionSourcePrimaryVersion(source)
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
		TotalWorkloads:    len(source.Spec.Units),
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
		Message:            fmt.Sprintf("%d/%d units ready", len(versions), len(source.Spec.Units)),
	})

	if err := r.Status().Patch(ctx, &mc, mcPatch); err != nil {
		l.Error(err, "failed to patch Cluster status", "cluster", cluster.Name)
	}

	return allReady
}

// cleanupRemovedClusters deletes Clusters and kubeconfig Secrets
// for clusters that were removed from the Kapro spec.
func (r *FleetReconciler) cleanupRemovedClusters(ctx context.Context, kapro *kaprov1alpha1.Fleet) {
	l := log.FromContext(ctx)

	// Build set of current cluster names.
	current := map[string]bool{}
	for _, c := range kapro.Spec.Clusters {
		current[c.Name] = true
	}

	// Delete orphaned Clusters.
	var mcList kaprov1alpha1.ClusterList
	if err := r.List(ctx, &mcList); err == nil {
		for i := range mcList.Items {
			mc := &mcList.Items[i]
			// Only clean up Clusters that were created by this Kapro
			// (check if it's in our inventory).
			if !current[mc.Name] && isInInventory(kapro, "Cluster/"+mc.Name) {
				if err := r.Delete(ctx, mc); err != nil {
					l.Error(err, "failed to delete orphaned Cluster", "cluster", mc.Name)
				} else {
					l.Info("deleted orphaned Cluster", "cluster", mc.Name)
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

func isInInventory(kapro *kaprov1alpha1.Fleet, item string) bool {
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
func (r *FleetReconciler) ensureKubeconfigSecret(ctx context.Context, kapro *kaprov1alpha1.Fleet, cluster *kaprov1alpha1.ClusterRef) (string, error) {
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

func (r *FleetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Fleet{}).
		Watches(&kaprov1alpha1.Source{}, handler.EnqueueRequestsFromMapFunc(r.kaproSourceToKapro)).
		// Cluster.status.conditions[Ready] flips drive Phase=Unreachable
		// in the status sync step. Without this watch the heartbeat
		// reconciler's Ready=False reason=Unreachable transition would not
		// surface as Phase=Unreachable until an unrelated Kapro/PromotionSource
		// event fired. Predicate filters to Ready-condition transitions only —
		// no feedback loop with our own status patches (which don't touch
		// conditions[Ready]).
		Watches(&kaprov1alpha1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(r.fleetClusterToKapro),
			builder.WithPredicates(fleetClusterReadyConditionChangedPredicate{}),
		).
		Complete(r)
}

// kaproSourceToKapro maps a PromotionSource change to the Kapro(s) that reference it.
func (r *FleetReconciler) kaproSourceToKapro(ctx context.Context, obj client.Object) []reconcile.Request {
	source, ok := obj.(*kaprov1alpha1.Source)
	if !ok {
		return nil
	}
	var kapros kaprov1alpha1.FleetList
	if err := r.List(ctx, &kapros); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, k := range kapros.Items {
		if k.Spec.SourceRef == source.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&k),
			})
		}
	}
	return requests
}

// fleetClusterToKapro maps a Cluster change to the Kapro(s) whose
// spec.clusters references it by name. Matches the inventory ownership
// pattern used by cleanupRemovedClusters.
func (r *FleetReconciler) fleetClusterToKapro(ctx context.Context, obj client.Object) []reconcile.Request {
	fc, ok := obj.(*kaprov1alpha1.Cluster)
	if !ok {
		return nil
	}
	var kapros kaprov1alpha1.FleetList
	if err := r.List(ctx, &kapros); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, k := range kapros.Items {
		for _, c := range k.Spec.Clusters {
			if c.Name == fc.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&k),
				})
				break
			}
		}
	}
	return requests
}

// fleetClusterReadyConditionChangedPredicate triggers a reconcile only when a
// Cluster's ConditionTypeReady changes (status or reason). All other
// Cluster mutations — including our own Phase/CurrentVersions status
// writes — are filtered out so we don't feedback-loop on ourselves.
type fleetClusterReadyConditionChangedPredicate struct{}

func (fleetClusterReadyConditionChangedPredicate) Create(_ event.CreateEvent) bool { return false }
func (fleetClusterReadyConditionChangedPredicate) Delete(_ event.DeleteEvent) bool { return false }
func (fleetClusterReadyConditionChangedPredicate) Generic(_ event.GenericEvent) bool {
	return false
}
func (fleetClusterReadyConditionChangedPredicate) Update(e event.UpdateEvent) bool {
	oldFC, ok := e.ObjectOld.(*kaprov1alpha1.Cluster)
	if !ok {
		return false
	}
	newFC, ok := e.ObjectNew.(*kaprov1alpha1.Cluster)
	if !ok {
		return false
	}
	oldReady := apimeta.FindStatusCondition(oldFC.Status.Conditions, kaprov1alpha1.ConditionTypeReady)
	newReady := apimeta.FindStatusCondition(newFC.Status.Conditions, kaprov1alpha1.ConditionTypeReady)
	if oldReady == nil && newReady == nil {
		return false
	}
	if oldReady == nil || newReady == nil {
		return true
	}
	return oldReady.Status != newReady.Status || oldReady.Reason != newReady.Reason
}
