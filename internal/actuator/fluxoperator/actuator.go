// Package fluxoperator implements the Kapro Actuator Interface (KAI) for
// Flux Operator. Instead of patching individual Flux resources on spoke
// clusters, it patches ResourceSet inputs on the hub. Flux Operator renders
// the per-cluster Flux resources, and Flux syncs them to spokes.
//
// This is the default actuator for Kapro — no spoke-side kapro component needed.
package fluxoperator

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

var resourceSetGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSet",
}

// FluxOperatorActuator implements the KAI interface by patching Flux Operator
// ResourceSet inputs. Each MemberCluster maps to one input entry in a ResourceSet.
type FluxOperatorActuator struct {
	Client client.Client
}

var _ actuator.Actuator = (*FluxOperatorActuator)(nil)

// Apply patches the ResourceSet input for the target cluster with the desired version.
func (a *FluxOperatorActuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	l := log.FromContext(ctx)
	mc := req.Cluster
	if mc == nil {
		return fmt.Errorf("cluster is nil")
	}
	foSpec := mc.Spec.Actuator.FluxOperator
	if foSpec == nil {
		return fmt.Errorf("MemberCluster %q has no fluxOperator actuator config", mc.Name)
	}

	ns := foSpec.Namespace
	if ns == "" {
		ns = "flux-system"
	}
	inputField := foSpec.InputField
	if inputField == "" {
		inputField = "tag"
	}
	tenantField := foSpec.TenantField
	if tenantField == "" {
		tenantField = "tenant"
	}

	version := req.Version

	// Get the ResourceSet (unstructured to avoid hard flux-operator dependency).
	rs := &unstructured.Unstructured{}
	rs.SetGroupVersionKind(resourceSetGVK)
	if err := a.Client.Get(ctx, client.ObjectKey{Name: foSpec.ResourceSet, Namespace: ns}, rs); err != nil {
		return fmt.Errorf("get ResourceSet %s/%s: %w", ns, foSpec.ResourceSet, err)
	}

	// Parse and update inputs.
	patch := client.MergeFrom(rs.DeepCopy())
	inputs := getInputs(rs)
	found := false
	for i, input := range inputs {
		if getStr(input, tenantField) == mc.Name {
			input[inputField] = version
			inputs[i] = input
			found = true
			break
		}
	}
	if !found {
		inputs = append(inputs, map[string]any{
			tenantField: mc.Name,
			inputField:  version,
		})
	}
	setInputs(rs, inputs)

	if err := a.Client.Patch(ctx, rs, patch); err != nil {
		return fmt.Errorf("patch ResourceSet %s: %w", foSpec.ResourceSet, err)
	}

	l.Info("patched ResourceSet input",
		"resourceSet", foSpec.ResourceSet, "cluster", mc.Name, "version", version)
	return nil
}

// IsConverged checks if the ResourceSet is Ready and the input matches desired version.
func (a *FluxOperatorActuator) IsConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, appKey, version string) (bool, error) {
	foSpec := mc.Spec.Actuator.FluxOperator
	if foSpec == nil {
		return false, fmt.Errorf("no fluxOperator config on %s", mc.Name)
	}
	ns := foSpec.Namespace
	if ns == "" {
		ns = "flux-system"
	}
	inputField := foSpec.InputField
	if inputField == "" {
		inputField = "tag"
	}
	tenantField := foSpec.TenantField
	if tenantField == "" {
		tenantField = "tenant"
	}

	rs := &unstructured.Unstructured{}
	rs.SetGroupVersionKind(resourceSetGVK)
	if err := a.Client.Get(ctx, client.ObjectKey{Name: foSpec.ResourceSet, Namespace: ns}, rs); err != nil {
		return false, err
	}

	// Check Ready condition.
	if !isReady(rs) {
		return false, nil
	}

	// Check that the input for this cluster has the desired version.
	for _, input := range getInputs(rs) {
		if getStr(input, tenantField) == mc.Name {
			return getStr(input, inputField) == version, nil
		}
	}
	return false, nil
}

// IsAllConverged checks convergence for all desired versions.
func (a *FluxOperatorActuator) IsAllConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, desiredVersions map[string]string) (bool, error) {
	if mc == nil {
		return false, fmt.Errorf("cluster is nil")
	}
	for _, version := range desiredVersions {
		converged, err := a.IsConverged(ctx, mc, "", version)
		if err != nil || !converged {
			return false, err
		}
	}
	return true, nil
}

// Rollback sets the ResourceSet input back to a previous version.
func (a *FluxOperatorActuator) Rollback(ctx context.Context, mc *kaprov1alpha1.MemberCluster, appKey, previousVersion string) error {
	if mc == nil {
		return fmt.Errorf("cluster is nil")
	}
	return a.Apply(ctx, actuator.ApplyRequest{Cluster: mc, Version: previousVersion})
}

// ApplyDelta applies version changes for multiple artifacts.
func (a *FluxOperatorActuator) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("cluster is nil")
	}
	count := 0
	for _, version := range req.DesiredVersions {
		if err := a.Apply(ctx, actuator.ApplyRequest{Cluster: req.Cluster, Version: version}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// --- Unstructured helpers ---

func getInputs(rs *unstructured.Unstructured) []map[string]any {
	spec, _ := rs.Object["spec"].(map[string]any)
	if spec == nil {
		return nil
	}
	raw, _ := spec["inputs"].([]any)
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result
}

func setInputs(rs *unstructured.Unstructured, inputs []map[string]any) {
	spec, _ := rs.Object["spec"].(map[string]any)
	if spec == nil {
		spec = make(map[string]any)
		rs.Object["spec"] = spec
	}
	items := make([]any, len(inputs))
	for i, m := range inputs {
		items[i] = m
	}
	spec["inputs"] = items
}

func getStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func isReady(rs *unstructured.Unstructured) bool {
	status, _ := rs.Object["status"].(map[string]any)
	if status == nil {
		return false
	}
	conditions, _ := status["conditions"].([]any)
	for _, c := range conditions {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		if fmt.Sprintf("%v", cm["type"]) == "Ready" && fmt.Sprintf("%v", cm["status"]) == "True" {
			return true
		}
	}
	return false
}
