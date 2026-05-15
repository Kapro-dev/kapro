// Package spoke implements the Kapro Actuator Interface for pull-mode delivery.
//
// Pull-mode promotion is intentionally hub-to-CRD, not hub-to-spoke. The hub
// writes desired versions onto MemberCluster.spec; the spoke-side controller
// observes that desired state, patches local GitOps resources, and reports
// convergence back through MemberCluster.status plus the heartbeat Lease.
package spoke

import (
	"context"
	"fmt"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

// DesiredStateActuator implements pull-mode delivery by updating hub-side
// MemberCluster desired state. It never connects to spoke clusters directly.
type DesiredStateActuator struct {
	// HubClient is the controller-runtime client for the hub cluster.
	// Used to patch MemberCluster.spec.desiredVersions.
	HubClient client.Client
}

// SpokeFluxActuator is kept as a compatibility alias for older internal code.
type SpokeFluxActuator = DesiredStateActuator

var _ actuator.Actuator = (*DesiredStateActuator)(nil)

// Apply records one desired version on the MemberCluster. The spoke-side
// controller owns applying it to local Flux resources.
func (a *DesiredStateActuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	mc := req.Cluster
	if mc == nil {
		return fmt.Errorf("cluster is nil")
	}
	appKey := req.AppKey
	if appKey == "" {
		appKey = "default"
	}
	_, err := a.ApplyDelta(ctx, actuator.DeltaApplyRequest{
		Cluster:         mc,
		DesiredVersions: map[string]string{appKey: req.Version},
	})
	return err
}

// ApplyDelta records all desired artifact versions on the MemberCluster in one
// patch. The spoke controller applies only changed entries locally.
func (a *DesiredStateActuator) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("cluster is nil")
	}
	if a.HubClient == nil {
		return 0, fmt.Errorf("hub client is nil")
	}

	desired := normalizeDesiredVersions(req.DesiredVersions)
	if len(desired) == 0 {
		return 0, nil
	}

	var latest kaprov1alpha1.MemberCluster
	if err := a.HubClient.Get(ctx, client.ObjectKey{Name: req.Cluster.Name}, &latest); err != nil {
		return 0, fmt.Errorf("get MemberCluster %s: %w", req.Cluster.Name, err)
	}

	count := 0
	for appKey, version := range desired {
		if latest.Spec.DesiredVersions[appKey] != version {
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}

	patch := client.MergeFrom(latest.DeepCopy())
	if latest.Spec.DesiredVersions == nil {
		latest.Spec.DesiredVersions = map[string]string{}
	}
	for appKey, version := range desired {
		latest.Spec.DesiredVersions[appKey] = version
	}
	primaryVersion, primaryAppKey := primaryDesiredVersion(desired)
	latest.Spec.DesiredVersion = primaryVersion
	latest.Spec.DesiredAppKey = primaryAppKey

	if err := a.HubClient.Patch(ctx, &latest, patch); err != nil {
		return 0, fmt.Errorf("patch MemberCluster %s desired versions: %w", latest.Name, err)
	}
	log.FromContext(ctx).Info("recorded pull-mode desired versions",
		"cluster", latest.Name, "changed", count, "desiredVersions", desired)
	return count, nil
}

// IsConverged checks the spoke-reported MemberCluster status.
func (a *DesiredStateActuator) IsConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, appKey, version string) (bool, error) {
	if appKey == "" {
		appKey = "default"
	}
	return a.IsAllConverged(ctx, mc, map[string]string{appKey: version})
}

// IsAllConverged checks the spoke-reported version map and health summary.
func (a *DesiredStateActuator) IsAllConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, desiredVersions map[string]string) (bool, error) {
	_ = ctx
	if mc == nil {
		return false, fmt.Errorf("cluster is nil")
	}
	for appKey, desired := range normalizeDesiredVersions(desiredVersions) {
		if mc.Status.CurrentVersions[appKey] != desired {
			return false, nil
		}
	}
	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
		return false, fmt.Errorf("cluster %s reported Failed phase", mc.Name)
	}
	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseConverged {
		return true, nil
	}
	return mc.Status.Health.AllWorkloadsReady, nil
}

// Rollback records the previous version as desired state.
func (a *DesiredStateActuator) Rollback(ctx context.Context, mc *kaprov1alpha1.MemberCluster, previousVersion, appKey string) error {
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster: mc,
		Version: previousVersion,
		AppKey:  appKey,
	})
}

func normalizeDesiredVersions(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for appKey, version := range in {
		if version == "" {
			continue
		}
		if appKey == "" {
			appKey = "default"
		}
		out[appKey] = version
	}
	return out
}

func primaryDesiredVersion(desired map[string]string) (version, appKey string) {
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", ""
	}
	if _, ok := desired["default"]; ok {
		return desired["default"], "default"
	}
	appKey = keys[0]
	return desired[appKey], appKey
}
