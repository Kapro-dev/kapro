package controller

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

var resourceSetGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSet",
}

// FleetReconciler generates Flux Operator resources from a Fleet spec.
// It produces:
//   - MemberCluster CRs (one per fleet.spec.clusters entry)
//   - A Pipeline CR (from fleet.spec.pipeline)
//   - A ResourceSet (Flux Operator) with HelmRelease templates
//
// Fleet generates the ResourceSet TEMPLATE (spec.resources[]), but Release
// patches the ResourceSet INPUTS (versions per wave). Fleet NEVER writes
// version data — it writes the template structure only. The version in inputs
// is the INITIAL version from Fleet.spec.components[].version.
type FleetReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

func (r *FleetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var fleet kaprov1alpha1.Fleet
	if err := r.Get(ctx, req.NamespacedName, &fleet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if fleet.Spec.Suspended {
		l.Info("Fleet is suspended, skipping")
		return ctrl.Result{}, nil
	}

	l.Info("reconciling Fleet", "name", fleet.Name)

	// 1. Generate MemberClusters
	for _, cluster := range fleet.Spec.Clusters {
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
						ResourceSet: fleet.Name + "-workloads",
						Namespace:   "flux-system",
						InputField:  "version",
						TenantField: "component",
					},
				},
			},
		}
		if err := r.Patch(ctx, mc,
			client.Apply,
			client.FieldOwner("kapro-fleet-controller"),
			client.ForceOwnership,
		); err != nil {
			l.Error(err, "failed to apply MemberCluster", "cluster", cluster.Name)
			// continue — don't fail the whole reconcile for one cluster
		}
	}

	// 2. Generate Pipeline
	pipeline := r.buildPipeline(&fleet)
	if err := r.Patch(ctx, pipeline,
		client.Apply,
		client.FieldOwner("kapro-fleet-controller"),
		client.ForceOwnership,
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply Pipeline: %w", err)
	}

	// 3. Generate ResourceSet (the core — template for all components)
	rs := r.buildResourceSet(&fleet)
	if err := r.Patch(ctx, rs,
		client.Apply,
		client.FieldOwner("kapro-fleet-controller"),
		client.ForceOwnership,
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply ResourceSet: %w", err)
	}

	// 4. Update Fleet status
	patch := client.MergeFrom(fleet.DeepCopy())
	fleet.Status.ClusterCount = int32(len(fleet.Spec.Clusters))
	fleet.Status.ComponentCount = int32(len(fleet.Spec.Components))
	fleet.Status.ObservedGeneration = fleet.Generation
	apimeta.SetStatusCondition(&fleet.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: fleet.Generation,
		Reason:             "ReconcileSuccess",
		Message:            fmt.Sprintf("Generated %d clusters, %d components", len(fleet.Spec.Clusters), len(fleet.Spec.Components)),
	})
	if err := r.Status().Patch(ctx, &fleet, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Fleet status: %w", err)
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *FleetReconciler) buildPipeline(fleet *kaprov1alpha1.Fleet) *kaprov1alpha1.Pipeline {
	stages := make([]kaprov1alpha1.Stage, 0, len(fleet.Spec.Pipeline.Stages))
	for _, s := range fleet.Spec.Pipeline.Stages {
		stage := kaprov1alpha1.Stage{
			Name: s.Name,
			Selector: metav1.LabelSelector{
				MatchLabels: s.Selector,
			},
		}
		for _, dep := range s.DependsOn {
			stage.DependsOn = append(stage.DependsOn, kaprov1alpha1.StageDependency{Stage: dep})
		}
		stages = append(stages, stage)
	}
	return &kaprov1alpha1.Pipeline{
		TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Pipeline"},
		ObjectMeta: metav1.ObjectMeta{
			Name: fleet.Name + "-pipeline",
		},
		Spec: kaprov1alpha1.PipelineSpec{Stages: stages},
	}
}

func (r *FleetReconciler) buildResourceSet(fleet *kaprov1alpha1.Fleet) *unstructured.Unstructured {
	// Build inputs: one entry per component with name + version + dependsOn.
	// The version here is the INITIAL version from Fleet.spec.components[].version.
	// Release controller patches inputs when promoting new versions per wave.
	inputs := make([]interface{}, 0, len(fleet.Spec.Components))
	for _, comp := range fleet.Spec.Components {
		input := map[string]interface{}{
			"component": comp.Name,
			"version":   comp.Version,
		}
		if comp.DependsOn != "" {
			input["dependsOn"] = comp.DependsOn
		}
		inputs = append(inputs, input)
	}

	// Build the HelmRelease template resource.
	// Template strings use << >> delimiters — stored as literal strings.
	// Flux Operator interprets them at apply time.
	helmReleaseTemplate := map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]interface{}{
			"name":      "<< inputs.component >>",
			"namespace": "flux-system",
		},
		"spec": map[string]interface{}{
			"interval": "10m",
			"chart": map[string]interface{}{
				"spec": map[string]interface{}{
					"chart": "<< inputs.component >>",
					"sourceRef": map[string]interface{}{
						"kind": "HelmRepository",
						"name": fleet.Spec.Registry.Name,
					},
					"version": "<< inputs.version >>",
				},
			},
			"targetNamespace": "${namespace}",
			"releaseName":     "<< inputs.component >>",
			"install": map[string]interface{}{
				"timeout": "5m",
				"remediation": map[string]interface{}{
					"retries": 3,
				},
			},
			"upgrade": map[string]interface{}{
				"timeout": "5m",
				"remediation": map[string]interface{}{
					"retries": 3,
				},
			},
			"valuesFrom": []interface{}{
				map[string]interface{}{
					"kind":     "ConfigMap",
					"name":     "helm-values-global",
					"optional": true,
				},
			},
		},
	}

	rs := &unstructured.Unstructured{}
	rs.SetGroupVersionKind(resourceSetGVK)
	rs.SetName(fleet.Name + "-workloads")
	rs.SetNamespace("flux-system")
	rs.SetAnnotations(map[string]string{
		"fluxcd.controlplane.io/reconcile":      "enabled",
		"fluxcd.controlplane.io/reconcileEvery": "5m",
	})
	rs.Object["apiVersion"] = "fluxcd.controlplane.io/v1"
	rs.Object["kind"] = "ResourceSet"
	rs.Object["spec"] = map[string]interface{}{
		"inputs":    inputs,
		"resources": []interface{}{helmReleaseTemplate},
	}

	return rs
}

func (r *FleetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Fleet{}).
		Complete(r)
}
