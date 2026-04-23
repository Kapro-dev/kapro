package flux

import (
	"context"
	"fmt"
	"time"

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
	appKey := resolveAppKey(req.AppKey)
	log := log.FromContext(ctx).WithValues(
		"cluster", req.Cluster.Name,
		"version", req.Version,
		"appKey", appKey,
	)

	mc := req.Cluster
	if mc.Spec.DesiredVersion == req.Version && mc.Spec.DesiredAppKey == appKey {
		log.Info("desiredVersion+appKey already set, skipping patch")
		return nil
	}

	patch := client.MergeFrom(mc.DeepCopy())
	mc.Spec.DesiredVersion = req.Version
	mc.Spec.DesiredAppKey = appKey
	if err := a.Client.Patch(ctx, mc, patch); err != nil {
		return fmt.Errorf("FluxActuator.Apply: patch MemberCluster %s: %w", mc.Name, err)
	}

	ociRepo := ""
	if mc.Spec.Actuator.Flux != nil {
		ociRepo = mc.Spec.Actuator.Flux.OCIRepository
	}
	log.Info("patched MemberCluster.spec.desiredVersion",
		"ociRepo", ociRepo,
	)
	return nil
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
	if !isHeartbeatFresh(cluster.Status.LastHeartbeat) {
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
func (a *FluxActuator) Rollback(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, previousVersion string) error {
	if cluster == nil {
		return fmt.Errorf("FluxActuator.Rollback: cluster is nil")
	}
	log.FromContext(ctx).Info("rolling back",
		"cluster", cluster.Name,
		"previousVersion", previousVersion,
	)
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster:  cluster,
		Version:  previousVersion,
		AppKey:   cluster.Spec.DesiredAppKey,
	})
}

// resolveAppKey returns appKey if non-empty, otherwise "default".
func resolveAppKey(appKey string) string {
	if appKey != "" {
		return appKey
	}
	return "default"
}

func isHeartbeatFresh(lastHeartbeat string) bool {
	if lastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, lastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < 2*time.Minute
}
