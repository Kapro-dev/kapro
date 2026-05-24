// Package fluxoperator implements the Kapro Actuator Interface (KAI) for
// Flux Operator. Instead of patching individual Flux resources on spoke
// clusters, it patches ResourceSet inputs on the hub. Flux Operator renders
// the per-cluster Flux resources, and Flux syncs them to spokes.
//
// This is the default actuator for Kapro — no spoke-side kapro unit needed.
package fluxoperator

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

var resourceSetGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSet",
}

// FluxOperatorActuator implements the KAI interface by patching Flux Operator
// ResourceSet inputs. Each FleetCluster maps to one input entry in a ResourceSet.
//
// Input field naming convention:
//   - Single-app: inputField (default "tag") holds the version
//   - Multi-unit (PromotionSource): "{appKey}_version" per unit (e.g. "pos-server_version")
//
// The actuator resolves the field name from appKey: if appKey is non-empty and
// the input entry has a matching "{appKey}_version" field, it patches that.
// Otherwise falls back to the configured inputField for backward compatibility.
type FluxOperatorActuator struct {
	Client client.Client
}

var _ actuator.Actuator = (*FluxOperatorActuator)(nil)

// Apply patches the ResourceSet input for the target cluster with the desired version.
// appKey determines which version field to patch:
//   - appKey="pos-server" → patches "pos-server_version" field
//   - appKey="" or "default" → patches the configured inputField (backward compat)
func (a *FluxOperatorActuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	l := log.FromContext(ctx)
	mc := req.Cluster
	if mc == nil {
		return fmt.Errorf("cluster is nil")
	}
	delivery := mc.Spec.Delivery

	ns, tenantField := resolveConfig(&delivery)
	resourceSet := delivery.Param("resourceSet", "")
	if resourceSet == "" {
		return fmt.Errorf("FleetCluster %q delivery.parameters.resourceSet is required for flux push delivery", mc.Name)
	}
	versionField := resolveVersionField(&delivery, req.AppKey)

	rs, err := a.getResourceSet(ctx, resourceSet, ns)
	if err != nil {
		return err
	}

	patch := client.MergeFrom(rs.DeepCopy())
	inputs := getInputs(rs)
	found := false
	for i, input := range inputs {
		if getStr(input, tenantField) == mc.Name {
			input[versionField] = req.Version
			inputs[i] = input
			found = true
			break
		}
	}
	if !found {
		inputs = append(inputs, map[string]any{
			tenantField:  mc.Name,
			versionField: req.Version,
		})
	}
	setInputs(rs, inputs)

	if err := a.Client.Patch(ctx, rs, patch); err != nil {
		return fmt.Errorf("patch ResourceSet %s: %w", resourceSet, err)
	}

	l.Info("patched ResourceSet input",
		"resourceSet", resourceSet, "cluster", mc.Name,
		"field", versionField, "version", req.Version)
	return nil
}

// ApplyDelta applies version changes for multiple artifacts in a single ResourceSet patch.
// More efficient than calling Apply per artifact — one GET + one PATCH.
func (a *FluxOperatorActuator) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("cluster is nil")
	}
	mc := req.Cluster
	delivery := mc.Spec.Delivery
	resourceSet := delivery.Param("resourceSet", "")
	if resourceSet == "" {
		return 0, fmt.Errorf("FleetCluster %q delivery.parameters.resourceSet is required for flux push delivery", mc.Name)
	}

	ns, tenantField := resolveConfig(&delivery)

	rs, err := a.getResourceSet(ctx, resourceSet, ns)
	if err != nil {
		return 0, err
	}

	patch := client.MergeFrom(rs.DeepCopy())
	inputs := getInputs(rs)

	// Find the input entry for this cluster.
	idx := -1
	for i, input := range inputs {
		if getStr(input, tenantField) == mc.Name {
			idx = i
			break
		}
	}
	if idx == -1 {
		// Create new entry.
		entry := map[string]any{tenantField: mc.Name}
		for appKey, version := range req.DesiredVersions {
			entry[resolveVersionField(&delivery, appKey)] = version
		}
		inputs = append(inputs, entry)
		setInputs(rs, inputs)
		if err := a.Client.Patch(ctx, rs, patch); err != nil {
			return 0, fmt.Errorf("patch ResourceSet %s: %w", resourceSet, err)
		}
		return len(req.DesiredVersions), nil
	}

	// Update existing entry — patch all changed version fields at once.
	count := 0
	for appKey, version := range req.DesiredVersions {
		field := resolveVersionField(&delivery, appKey)
		if getStr(inputs[idx], field) != version {
			inputs[idx][field] = version
			count++
		}
	}

	if count == 0 {
		return 0, nil
	}

	setInputs(rs, inputs)
	if err := a.Client.Patch(ctx, rs, patch); err != nil {
		return 0, fmt.Errorf("patch ResourceSet %s: %w", resourceSet, err)
	}

	l := log.FromContext(ctx)
	l.Info("patched ResourceSet inputs (delta)",
		"resourceSet", resourceSet, "cluster", mc.Name, "changed", count)
	return count, nil
}

// IsConverged checks if the ResourceSet input matches AND the rendered HelmRelease is Ready.
// ResourceSet Ready only means "YAML was applied" — we also need to verify the spoke
// HelmRelease actually succeeded (Ready=True on the HelmRelease itself).
func (a *FluxOperatorActuator) IsConverged(ctx context.Context, mc *kaprov1alpha1.Cluster, appKey, version string) (bool, error) {
	delivery := mc.Spec.Delivery
	resourceSet := delivery.Param("resourceSet", "")
	if resourceSet == "" {
		return false, fmt.Errorf("FleetCluster %q delivery.parameters.resourceSet is required for flux push delivery", mc.Name)
	}

	ns, tenantField := resolveConfig(&delivery)
	versionField := resolveVersionField(&delivery, appKey)

	rs, err := a.getResourceSet(ctx, resourceSet, ns)
	if err != nil {
		return false, err
	}

	if !isReady(rs) {
		return false, nil
	}

	// Check ResourceSet input has the right version.
	inputMatch := false
	for _, input := range getInputs(rs) {
		if getStr(input, tenantField) == mc.Name {
			if getStr(input, versionField) == version {
				inputMatch = true
			}
			break
		}
	}
	if !inputMatch {
		return false, nil
	}

	// Check the rendered HelmRelease is Ready.
	// Try to find it by scanning ResourceSet inventory.
	hrReady, err := a.checkHelmReleaseFromInventory(ctx, resourceSet, ns, mc.Name)
	if err != nil {
		// Can't determine HR name — fall through to FleetCluster status check
		// in the TargetReconciler fallback.
		return false, nil
	}
	return hrReady, nil
}

// IsAllConverged checks convergence for all desired versions.
// Verifies both ResourceSet inputs AND rendered HelmRelease Ready status.
func (a *FluxOperatorActuator) IsAllConverged(ctx context.Context, mc *kaprov1alpha1.Cluster, desiredVersions map[string]string) (bool, error) {
	if mc == nil {
		return false, fmt.Errorf("cluster is nil")
	}
	delivery := mc.Spec.Delivery
	resourceSet := delivery.Param("resourceSet", "")
	if resourceSet == "" {
		return false, fmt.Errorf("FleetCluster %q delivery.parameters.resourceSet is required for flux push delivery", mc.Name)
	}

	ns, tenantField := resolveConfig(&delivery)

	rs, err := a.getResourceSet(ctx, resourceSet, ns)
	if err != nil {
		return false, err
	}

	if !isReady(rs) {
		return false, nil
	}

	// Check all inputs match.
	for _, input := range getInputs(rs) {
		if getStr(input, tenantField) != mc.Name {
			continue
		}
		for appKey, version := range desiredVersions {
			field := resolveVersionField(&delivery, appKey)
			if getStr(input, field) != version {
				return false, nil
			}
		}

		// All inputs match — now check HelmReleases are Ready via inventory.
		hrReady, err := a.checkHelmReleaseFromInventory(ctx, resourceSet, ns, mc.Name)
		if err != nil || !hrReady {
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

// Rollback sets the ResourceSet input back to a previous version.
func (a *FluxOperatorActuator) Rollback(ctx context.Context, mc *kaprov1alpha1.Cluster, previousVersion, appKey string) error {
	if mc == nil {
		return fmt.Errorf("cluster is nil")
	}
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster: mc,
		Version: previousVersion,
		AppKey:  appKey,
	})
}

// --- Config helpers ---

func resolveConfig(delivery *kaprov1alpha1.DeliverySpec) (ns, tenantField string) {
	return delivery.Param("namespace", "flux-system"), delivery.Param("tenantField", "tenant")
}

// resolveVersionField maps an appKey to the ResourceSet input field name.
// For multi-unit PromotionSource: "pos-server" → "pos-server_version"
// For single-app (backward compat): "" or "default" → configured inputField
func resolveVersionField(delivery *kaprov1alpha1.DeliverySpec, appKey string) string {
	if appKey != "" && appKey != "default" {
		return appKey + "_version"
	}
	return delivery.Param("inputField", "tag")
}

func (a *FluxOperatorActuator) getResourceSet(ctx context.Context, name, ns string) (*unstructured.Unstructured, error) {
	rs := &unstructured.Unstructured{}
	rs.SetGroupVersionKind(resourceSetGVK)
	if err := a.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, rs); err != nil {
		return nil, fmt.Errorf("get ResourceSet %s/%s: %w", ns, name, err)
	}
	return rs, nil
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

func isReady(obj *unstructured.Unstructured) bool {
	return hasReadyCondition(obj)
}

// hasReadyCondition checks if an unstructured object has condition type=Ready, status=True.
func hasReadyCondition(obj *unstructured.Unstructured) bool {
	status, _ := obj.Object["status"].(map[string]any)
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

var helmReleaseGVK = schema.GroupVersionKind{
	Group:   "helm.toolkit.fluxcd.io",
	Version: "v2",
	Kind:    "HelmRelease",
}

// resolveHelmReleaseName returns the HelmRelease name for a unit on a cluster.
// Convention from buildHelmRelease: {unitName}-{clusterName}
// checkHelmReleaseFromInventory finds the HelmRelease for a cluster in the ResourceSet inventory.
func (a *FluxOperatorActuator) checkHelmReleaseFromInventory(ctx context.Context, rsName, ns, clusterName string) (bool, error) {
	rs, err := a.getResourceSet(ctx, rsName, ns)
	if err != nil {
		return false, err
	}
	// Scan inventory for HelmRelease entries matching this cluster.
	status, _ := rs.Object["status"].(map[string]any)
	if status == nil {
		return false, fmt.Errorf("no ResourceSet status")
	}
	inv, _ := status["inventory"].(map[string]any)
	if inv == nil {
		return false, fmt.Errorf("no inventory")
	}
	entries, _ := inv["entries"].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		if entry == nil {
			continue
		}
		id, _ := entry["id"].(string)
		// Inventory ID format: namespace_name_group_kind
		if !strings.Contains(id, "HelmRelease") {
			continue
		}
		if !strings.Contains(id, clusterName) {
			continue
		}
		// Extract name: flux-system_hello-kapro-spoke_helm.toolkit.fluxcd.io_HelmRelease
		parts := strings.SplitN(id, "_", 3)
		if len(parts) < 2 {
			continue
		}
		hrName := parts[1]
		return a.isHelmReleaseReady(ctx, hrName, ns)
	}
	return false, fmt.Errorf("no HelmRelease found for %s in inventory", clusterName)
}

// isHelmReleaseReady checks if the rendered HelmRelease has Ready=True.
func (a *FluxOperatorActuator) isHelmReleaseReady(ctx context.Context, name, ns string) (bool, error) {
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(helmReleaseGVK)
	if err := a.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, hr); err != nil {
		return false, fmt.Errorf("get HelmRelease %s/%s: %w", ns, name, err)
	}
	return hasReadyCondition(hr), nil
}
