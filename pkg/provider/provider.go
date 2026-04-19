// Package provider defines KCI — the Kapro Cluster Interface.
//
// KCI v1alpha1 is the pluggable cluster connectivity contract. Kapro uses it to
// resolve workload cluster access so actuators, health assessors, and gate
// evaluators can interact with the target cluster.
//
// # Two-path model
//
// KCI is intentionally split into two interfaces matching the two onboarding paths:
//
//	Connector (Path B — direct connect)
//	  The hub establishes a direct HTTPS connection to the spoke API server.
//	  Implementations use cloud IAM (Workload Identity, IRSA, Managed Identity)
//	  — no static credentials. Used for GKE, EKS, AKS, DigitalOcean, StackIT.
//
//	RegistrationReader (Path A — outbound/CRD)
//	  Kapro reads cluster state from ManagedCluster CRDs written by the
//	  kapro-cluster-controller running on each spoke. No hub→spoke network
//	  path required. The default for all clouds; works air-gapped.
//
// A provider implementation may implement one or both interfaces.
//
// # KCI contract
//
// Every KCI implementation must:
//   - Be safe for concurrent use
//   - Respect context cancellation and deadlines
//   - Return a descriptive error when the cluster is unreachable (not nil, nil)
//   - Never panic on a nil Environment argument (return error instead)
//   - Pass conformance/provider.RunSuite(t, impl)
//
// # Implementations
//
// Shipped:
//   - internal/provider/crd/ — RegistrationReader via ManagedCluster CRDs (Path A, all clouds)
//   - internal/provider/gke/ — Connector via Workload Identity (Path B, GKE)
//
// Tracked in docs/ROADMAP.md:
//   - internal/provider/aks/          — Azure Managed Identity + AAD OIDC federation
//   - internal/provider/digitalocean/ — DigitalOcean API token (Secret-referenced)
//   - internal/provider/stackit/      — StackIT Service Account key (Secret-referenced)
//
// # Stability
//
// KCI v1alpha1 is stable. Both interfaces have backwards-compatibility guarantees
// within a major version. The method signatures will not change.
package provider

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Connector is KCI-Connect: establishes a direct HTTPS connection to a workload
// cluster's Kubernetes API server.
//
// Implementations use cloud IAM where available (Workload Identity, IRSA,
// Managed Identity) — never store static credentials in CRD fields.
// Credentials are always read from Secrets referenced by name.
//
// Shipped: gke (Workload Identity). Tracked in ROADMAP.md: aks, digitalocean, stackit.
// Register implementations at startup in cmd/operator/main.go via provider.Registry.
type Connector interface {
	// Connect returns a *rest.Config for the given Environment's workload cluster.
	//
	// The returned config is short-lived: implementations that obtain tokens
	// via cloud IAM (STS, GKE token exchange) return a config whose token
	// may expire. Callers should call Connect fresh for each reconcile cycle
	// rather than caching the result.
	//
	// Must return (nil, error) — never (nil, nil).
	// Must return error when env is nil.
	Connect(ctx context.Context, env *kaprov1alpha1.Environment) (*rest.Config, error)

	// IsReachable returns true when the cluster API server responds to a
	// lightweight liveness probe. Called during HealthCheck before Apply.
	// Returns (false, nil) — not an error — when the cluster is temporarily
	// unreachable; the controller will retry on next reconcile.
	IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error)
}

// RegistrationReader is KCI-Register: reads cluster state from ManagedCluster
// CRDs written by kapro-cluster-controller (the outbound/CRD path).
//
// No direct network connection from hub to spoke is required.
// Used by internal/provider/crd/ — the default for all environments.
type RegistrationReader interface {
	// GetRegistration returns the ManagedCluster for the given Environment.
	// Returns an error if no ManagedCluster is found or if the heartbeat is stale.
	GetRegistration(ctx context.Context, env *kaprov1alpha1.Environment) (*kaprov1alpha1.ManagedCluster, error)
}

// NopConnector satisfies Connector for testing and environments that do not
// use the direct-connect path. Connect always returns an error — callers that
// reach it with a NopConnector have a configuration bug.
type NopConnector struct{}

func (NopConnector) Connect(_ context.Context, env *kaprov1alpha1.Environment) (*rest.Config, error) {
	if env == nil {
		return nil, fmt.Errorf("KCI NopConnector: environment is nil")
	}
	return nil, fmt.Errorf("KCI NopConnector: no Connector registered for environment %q (type %q) — register a cloud provider in cmd/operator/main.go",
		env.Name, env.Spec.Provider.Type)
}

func (NopConnector) IsReachable(_ context.Context, env *kaprov1alpha1.Environment) (bool, error) {
	if env == nil {
		return false, fmt.Errorf("KCI NopConnector: environment is nil")
	}
	return false, nil
}

// compile-time check: NopConnector satisfies Connector.
var _ Connector = NopConnector{}
