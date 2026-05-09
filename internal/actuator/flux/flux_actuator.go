package flux

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

// Compile-time assertion: FluxActuator must satisfy the Actuator interface.
var _ actuator.Actuator = (*FluxActuator)(nil)

// FluxActuator implements promotion via the CRD-native outbound pattern:
//
//  1. Apply() writes MemberCluster.spec.desiredVersion on the control plane.
//  2. kapro-cluster-controller on the workload cluster polls spec.desiredVersion
//     and patches the local OCIRepository — triggering Flux reconciliation.
//  3. IsConverged() reads MemberCluster.status.phase + currentVersions
//     to determine whether Flux has converged.
//
// No kubeconfig or inbound connection to workload clusters is needed.
type FluxActuator struct {
	// Client is the control-plane Kubernetes client.
	Client client.Client
}

// Apply sets MemberCluster.spec.desiredVersion (and desiredAppKey),
// signalling the cluster-controller to update the local OCIRepository.
func (a *FluxActuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	if req.Cluster == nil {
		return fmt.Errorf("FluxActuator.Apply: cluster is nil")
	}
	_, err := a.ApplyDelta(ctx, actuator.DeltaApplyRequest{
		Cluster: req.Cluster,
		DesiredVersions: map[string]string{
			resolveAppKey(req.AppKey): req.Version,
		},
	})
	return err
}

// IsConverged returns true when the workload cluster's cluster-controller
// has reconciled the desired version and Flux has converged.
//
// appKey is the key in MemberCluster.status.currentVersions to inspect.
// Use "default" for single-app clusters.
func (a *FluxActuator) IsConverged(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, version, appKey string) (bool, error) {
	if cluster == nil {
		return false, fmt.Errorf("FluxActuator.IsConverged: cluster is nil")
	}

	// Heartbeat must be fresh — stale means the cluster-controller is down.
	if !cluster.Status.IsHeartbeatFresh(2 * time.Minute) {
		return false, fmt.Errorf("cluster %s heartbeat is stale (last seen: %s)", cluster.Name, cluster.Status.LastHeartbeat)
	}

	resolvedKey := resolveAppKey(appKey)
	converged := cluster.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		cluster.Status.CurrentVersions[resolvedKey] == version

	log.FromContext(ctx).Info("convergence check",
		"cluster", cluster.Name,
		"appKey", resolvedKey,
		"phase", cluster.Status.Phase,
		"currentVersion", cluster.Status.CurrentVersions[resolvedKey],
		"wantVersion", version,
		"converged", converged,
	)

	return converged, nil
}

// Rollback sets the desired version back to the given (previous) version.
func (a *FluxActuator) Rollback(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, previousVersion, appKey string) error {
	if cluster == nil {
		return fmt.Errorf("FluxActuator.Rollback: cluster is nil")
	}
	log.FromContext(ctx).Info("rolling back",
		"cluster", cluster.Name,
		"previousVersion", previousVersion,
		"appKey", resolveAppKey(appKey),
	)
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster: cluster,
		Version: previousVersion,
		AppKey:  appKey,
	})
}

// ApplyDelta compares desiredVersions against MemberCluster.status.currentVersions
// and only applies artifacts that actually changed. Returns the delta count.
func (a *FluxActuator) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("FluxActuator.ApplyDelta: cluster is nil")
	}
	desired := normalizedDesiredVersions(req.DesiredVersions)
	if len(desired) == 0 {
		return 0, nil
	}
	if err := validateDesiredVersionTopology(req.Cluster, desired); err != nil {
		return 0, err
	}

	current := req.Cluster.Status.CurrentVersions
	if current == nil {
		current = make(map[string]string)
	}

	deltaCount := 0
	for appKey, version := range desired {
		if current[appKey] == version {
			log.FromContext(ctx).Info("artifact already converged, skipping",
				"cluster", req.Cluster.Name,
				"appKey", appKey,
				"version", version,
			)
			continue
		}
		deltaCount++
	}

	mc := req.Cluster
	if desiredVersionsEqual(mc.Spec.DesiredVersions, desired) && desiredVersionCompatibilityMatches(mc, desired) {
		log.FromContext(ctx).Info("desiredVersions already set, skipping patch", "cluster", mc.Name)
		return deltaCount, nil
	}

	patch := client.MergeFrom(mc.DeepCopy())
	mc.Spec.DesiredVersions = copyStringMap(desired)
	setLegacyDesiredVersionFields(mc, desired)
	if err := a.Client.Patch(ctx, mc, patch); err != nil {
		return deltaCount, fmt.Errorf("FluxActuator.ApplyDelta: patch MemberCluster %s: %w", mc.Name, err)
	}

	log.FromContext(ctx).Info("delta delivery complete",
		"cluster", req.Cluster.Name,
		"totalArtifacts", len(desired),
		"deltaApplied", deltaCount,
	)
	return deltaCount, nil
}

// IsAllConverged returns true when ALL artifacts in desiredVersions match
// the cluster's currentVersions.
func (a *FluxActuator) IsAllConverged(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, desiredVersions map[string]string) (bool, error) {
	if cluster == nil {
		return false, fmt.Errorf("FluxActuator.IsAllConverged: cluster is nil")
	}
	desired := normalizedDesiredVersions(desiredVersions)
	if len(desired) == 0 {
		return false, fmt.Errorf("FluxActuator.IsAllConverged: desiredVersions is empty")
	}
	if err := validateDesiredVersionTopology(cluster, desired); err != nil {
		return false, err
	}

	if !cluster.Status.IsHeartbeatFresh(2 * time.Minute) {
		return false, fmt.Errorf("cluster %s heartbeat is stale (last seen: %s)", cluster.Name, cluster.Status.LastHeartbeat)
	}

	for appKey, version := range desired {
		if cluster.Status.CurrentVersions[appKey] != version {
			log.FromContext(ctx).Info("artifact not converged",
				"cluster", cluster.Name,
				"appKey", appKey,
				"want", version,
				"have", cluster.Status.CurrentVersions[appKey],
			)
			return false, nil
		}
	}

	return cluster.Status.Phase == kaprov1alpha1.ClusterPhaseConverged, nil
}

// resolveAppKey returns appKey if non-empty, otherwise "default".
func resolveAppKey(appKey string) string {
	if appKey != "" {
		return appKey
	}
	return "default"
}

func normalizedDesiredVersions(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for appKey, version := range in {
		key := resolveAppKey(appKey)
		if version == "" {
			continue
		}
		out[key] = version
	}
	return out
}

func validateDesiredVersionTopology(cluster *kaprov1alpha1.MemberCluster, desired map[string]string) error {
	if len(desired) <= 1 {
		return nil
	}
	flux := cluster.Spec.Actuator.Flux
	if flux == nil {
		return fmt.Errorf("cluster %s actuator %q does not support multi-artifact delivery", cluster.Name, cluster.Spec.Actuator.Type)
	}
	if len(flux.OCIRepositories) == 0 {
		return fmt.Errorf("cluster %s flux actuator requires spec.actuator.flux.ociRepositories for multi-artifact delivery", cluster.Name)
	}
	var missing []string
	for appKey := range desired {
		if flux.OCIRepositories[appKey] == "" {
			missing = append(missing, appKey)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("cluster %s flux actuator is missing OCIRepository mappings for appKeys: %v", cluster.Name, missing)
}

func desiredVersionsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func desiredVersionCompatibilityMatches(mc *kaprov1alpha1.MemberCluster, desired map[string]string) bool {
	if len(desired) != 1 {
		return mc.Spec.DesiredVersion == "" && mc.Spec.DesiredAppKey == ""
	}
	for appKey, version := range desired {
		return mc.Spec.DesiredVersion == version && resolveAppKey(mc.Spec.DesiredAppKey) == appKey
	}
	return false
}

func setLegacyDesiredVersionFields(mc *kaprov1alpha1.MemberCluster, desired map[string]string) {
	if len(desired) != 1 {
		mc.Spec.DesiredVersion = ""
		mc.Spec.DesiredAppKey = ""
		return
	}
	for appKey, version := range desired {
		mc.Spec.DesiredVersion = version
		mc.Spec.DesiredAppKey = appKey
		return
	}
}

// Preflight checks that the Flux CRDs are installed on the cluster.
// Called at startup — fails fast with a clear message rather than
// letting reconcilers hit missing-resource errors at runtime.
func (a *FluxActuator) Preflight(cfg *rest.Config) error {
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build discovery client: %w", err)
	}
	groups, err := disc.ServerGroups()
	if err != nil {
		return fmt.Errorf("list API groups: %w", err)
	}

	required := map[string]bool{
		"source.toolkit.fluxcd.io":    false,
		"kustomize.toolkit.fluxcd.io": false,
	}
	for _, g := range groups.Groups {
		if _, ok := required[g.Name]; ok {
			required[g.Name] = true
		}
	}

	var missing []string
	for group, found := range required {
		if !found {
			missing = append(missing, group)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("flux actuator requires Flux CRDs: %s — run `flux install` first", strings.Join(missing, ", "))
	}
	return nil
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
