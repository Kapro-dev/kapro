// Package provider defines KCI — the Kapro Cluster Interface.
//
// KCI is the pluggable cluster connectivity contract. Kapro uses it to
// obtain a *rest.Config for workload clusters so actuators and health
// assessors can reach them.
//
// The interface is intentionally split into two concerns:
//
//   - Connector — for providers that establish a direct connection
//     (GKE via Workload Identity, EKS via IRSA, CAPI kubeconfig, Rancher)
//
//   - RegistrationReader — for providers that read connectivity info from
//     CRDs written by a cluster-side agent (kapro-cluster-controller pattern).
//     Used by the CRD provider — it has no direct network path to workload clusters.
//
// A Provider may implement one or both interfaces.
//
// Built-in implementations live in internal/provider/:
//   - capi/  — reads CAPI Cluster kubeconfig Secret → implements Connector
//   - ocm/   — reads OCM ManagedCluster kubeconfig Secret → implements Connector
//   - crd/   — reads ClusterRegistration CRDs → implements RegistrationReader
//
// External implementations (GKE, EKS, AKS, Rancher, k3s, Talos, Nutanix) register
// via PluginRegistration CRD and communicate over proto/kapro/v1alpha1/cluster.proto.
package provider

import (
	"context"

	"k8s.io/client-go/rest"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Connector is KCI-Connect: establishes a direct network connection to a workload cluster.
//
// Implementations: CAPI, OCM, GKE, EKS, AKS, Rancher.
type Connector interface {
	// Connect returns a *rest.Config for the given Environment's workload cluster.
	// Implementations should use Workload Identity where possible — no static credentials.
	Connect(ctx context.Context, env *kaprov1alpha1.Environment) (*rest.Config, error)

	// IsReachable returns true when the cluster API server responds to a health probe.
	// Used by the health check phase before applying a promotion.
	IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error)
}

// RegistrationReader is KCI-Register: reads cluster state from CRDs written by
// a cluster-side agent. No direct network connection required.
//
// Implementations: crd/ (kapro-cluster-controller heartbeat pattern).
type RegistrationReader interface {
	// GetRegistration returns the ClusterRegistration for the given Environment.
	// Returns an error if no registration is found or if it is stale.
	GetRegistration(ctx context.Context, env *kaprov1alpha1.Environment) (*kaprov1alpha1.ClusterRegistration, error)
}
