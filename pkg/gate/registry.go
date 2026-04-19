// Package gate — KGI registry for template-dispatch gate extensibility.
//
// GateTemplate.spec.type is the extension point: any string maps to a Gate
// implementation registered here. Built-in types (cel, job, webhook) are
// registered in BuildGateRegistry (pkg/controllermanager). External vendors
// register their own types at startup via MustRegister — no core code change
// required.
//
// This is the same pattern as actuator.Registry and provider.Registry:
// a thin struct embedding the generic Registry[T] from pkg/registry.
//
// # Kubernetes analogy
//
// GateRegistry is to gates what the CRI plugin registry is to container runtimes:
// it maps a type string to an implementation, discovered and registered at
// process startup, dispatched at runtime.
package gate

import pkgregistry "kapro.io/kapro/pkg/registry"

// Registry maps GateTemplate.spec.type strings to Gate implementations.
//
// The three built-in types are:
//   - "cel"     — CEL expression evaluation (internal/gate/cel)
//   - "job"     — Kubernetes Job runner    (internal/gate/job)
//   - "webhook" — HTTP callback            (internal/gate/webhook)
//
// External gate types register at startup:
//
//	cc.GateRegistry.MustRegister("argo-analysis", &mygate.ArgoAnalysisGate{...})
//
// Unknown types produce a descriptive error — never a panic.
type Registry struct {
	*pkgregistry.Registry[Gate]
}

// NewRegistry returns an empty, ready-to-use gate Registry.
// Populate it with BuildGateRegistry (pkg/controllermanager) or register
// gates manually for testing.
func NewRegistry() *Registry {
	return &Registry{pkgregistry.New[Gate]("gate")}
}
