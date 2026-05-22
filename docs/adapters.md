# Adapters

Kapro exposes a public Go adapter package at
`kapro.io/kapro/pkg/kapro/adapter`.

The package is an SDK boundary for delivery backends. It does not change the
current CRDs or controller wiring. Controllers still use the existing actuator,
spoke-provider, and Backend discovery paths until the rest of issue #144 wires
the public contract into runtime selection.

## Public Contract

An adapter owns backend-specific delivery work for one
`Backend.spec.driver` value:

```go
type Adapter interface {
	Driver() v1alpha2.BackendDriver
	Runtime() v1alpha2.BackendRuntime
	Apply(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Observe(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Rollback(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Discover(ctx context.Context, req adapter.DiscoveryRequest) (adapter.DiscoveryResult, error)
}
```

`Request` carries the PromotionRun identity, target cluster, selected Backend,
delivery mode, app key, version, previous version, desired version map, and
merged backend parameters.

`Result` normalizes delivery progress into `DeliveryPhase`, convergence,
changed count, digest, artifact format, applied object count, and optional
backend object status evidence.

`DiscoveryRequest` and `DiscoveryResult` model the observe/adopt discovery
shape used by `Backend.status.selectedObjects`, `skippedObjects`, and
`unsupportedPatterns`.

## Registry

`adapter.Registry` is thread-safe and resolves adapters by
`v1alpha2.BackendDriver`:

```go
reg := adapter.NewRegistry()
_ = reg.Register(flux.New())

backend, err := reg.Resolve(v1alpha2.BackendDriverFlux)
```

`Register` fails on nil, empty driver, or duplicate driver. `Upsert` is
available when replacement is intentional.

## Reference Adapters

Reference packages live under `pkg/kapro/adapter`:

| Package | Driver | Runtime | Current role |
|---|---|---|---|
| `adapter/argocd` | `argo` | `Hub` | Models Argo CD discovery. |
| `adapter/flux` | `flux` | `Both` | Models Flux discovery. |
| `adapter/oci` | `oci` | `Spoke` | Reports discovery unsupported because OCI delivery has no backend-native Kubernetes objects. |

The constructors are discovery-first and do not expose Kapro's legacy actuator
or spoke-provider packages through the public SDK:

```go
argoAdapter := argocd.New()
fluxAdapter := flux.New()
ociAdapter := oci.New()
```

Apply, observe, and rollback on these reference adapters return a failed
`OperationUnsupported` result. The operator continues to use the existing
runtime actuators for side effects until public adapter runtime selection is
wired in a later increment.

## Wiring Status

This package is intentionally additive. Full #144 wiring still needs controller
runtime registration to resolve `Backend.spec.driver` through
`adapter.Registry`, migration from direct actuator/spoke-provider registries
where appropriate, and conformance coverage for out-of-tree adapter authors.
