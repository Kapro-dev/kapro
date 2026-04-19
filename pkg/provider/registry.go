package provider

import (
	pkgregistry "kapro.io/kapro/pkg/registry"
)

// Registry resolves KCI (Kapro Cluster Interface) Connector implementations
// by provider type name.
//
// Type names match Environment.spec.provider.type (e.g. "gke", "eks", "aks").
// The registry is the runtime dispatch table between the connectivity backend
// declared in an Environment and the Connector implementation that handles it.
//
// # Dispatch semantics
//
// When Environment.spec.provider.type is "" (empty) or "crd", the caller
// uses the RegistrationReader path (kapro-cluster-controller heartbeat —
// no network path from hub to spoke). For all other type values, the
// Connector returned by Resolve() is used to obtain a *rest.Config directly.
//
//	"" or "crd"   → CRDProvider.GetRegistration() (RegistrationReader)
//	"gke"         → GKEConnector.Connect()          (Connector, v0.3)
//	"aks"         → AKSConnector.Connect()          (Connector, v0.4)
//	"digitalocean"→ DOConnector.Connect()           (Connector, v0.4)
//	"stackit"     → StackITConnector.Connect()      (Connector, v0.4)
//
// # Registering a cloud connector
//
//	reg := provider.NewRegistry()
//	// v0.3+:
//	reg.MustRegister("gke", &gkeprovider.Connector{Client: mgr.GetClient()})
//
// # Resolving a connector at reconcile time
//
//	conn, err := reg.Resolve(env.Spec.Provider.Type)
//	// conn is nil when type is "" or "crd" — caller uses CRD path instead.
//
// All methods are safe for concurrent use.
type Registry struct {
	*pkgregistry.Registry[Connector]
}

// NewRegistry returns a new, empty provider Registry.
// In MVP, no connectors are registered — all environments use the CRD path.
// Cloud connectors (gke, eks, aks, digitalocean, stackit) are registered in v0.3+.
func NewRegistry() *Registry {
	return &Registry{Registry: pkgregistry.New[Connector]("provider")}
}

// IsCRDPath returns true when envProviderType indicates the CRD-based
// outbound path should be used (kapro-cluster-controller heartbeat).
// This is the default when no provider type is specified.
func IsCRDPath(envProviderType string) bool {
	return envProviderType == "" || envProviderType == "crd"
}
