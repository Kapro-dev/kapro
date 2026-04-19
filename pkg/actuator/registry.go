package actuator

import (
	pkgregistry "kapro.io/kapro/pkg/registry"
)

// Registry resolves KAI (Kapro Actuator Interface) implementations by type name.
//
// Type names match Environment.spec.actuator.type (e.g. "flux", "argocd").
// The registry is the runtime dispatch table between the delivery type declared
// in an Environment and the Actuator implementation that handles it.
//
// This is analogous to the kubelet's container runtime registry: Kubernetes does
// not hard-code Docker or containerd; it resolves the runtime from the node
// config at pod-scheduling time. Kapro does not hard-code Flux or ArgoCD; it
// resolves the actuator from Environment.spec.actuator.type at sync time.
//
// # Registering an actuator
//
//	reg := actuator.NewRegistry()
//	reg.MustRegister("flux", &fluxactuator.FluxActuator{Client: mgr.GetClient()})
//	// v0.3+:
//	reg.MustRegister("argocd", &argocdactuator.Actuator{Client: mgr.GetClient()})
//
// # Resolving an actuator at reconcile time
//
//	impl, err := reg.Resolve(env.Spec.Actuator.Type)
//
// All methods are safe for concurrent use.
type Registry struct {
	*pkgregistry.Registry[Actuator]
}

// NewRegistry returns a new, empty actuator Registry.
func NewRegistry() *Registry {
	return &Registry{Registry: pkgregistry.New[Actuator]("actuator")}
}
