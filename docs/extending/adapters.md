# Adapters

Kapro exposes a public Go adapter package at
`kapro.io/kapro/pkg/kapro/adapter`.

The package is an SDK boundary for delivery backends. AdapterPolicy discovery
uses this public registry to resolve `Backend.spec.driver` and call
`Adapter.Discover`. Delivery side effects still use the existing actuator and
spoke-provider paths until runtime selection moves to the public contract.

## Public Contract

An adapter owns backend-specific delivery work for one
`Backend.spec.driver` value:

```go
type Adapter interface {
	Driver() v1alpha2.BackendDriver
	Runtime() v1alpha2.BackendRuntime
	Capabilities() adapter.Capabilities
	Apply(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Observe(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Rollback(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Discover(ctx context.Context, req adapter.DiscoveryRequest) (adapter.DiscoveryResult, error)
}
```

`Capabilities` is the first thing controllers should read. It tells callers
which methods are meaningful for a backend so unsupported operations do not
become normal error-path control flow. See
[Adapter Capabilities](adapter-capabilities.md) for each bit.

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

The reference adapters advertise discovery-only capabilities. Apply, observe,
and rollback return a failed `OperationUnsupported` result for direct callers,
and controllers branch on the capability bits before calling unsupported
operations. The operator continues to use the existing runtime actuators for
side effects until public adapter runtime selection is wired in a later
increment.

## AdapterPolicy Discovery

`AdapterPolicy` runs a continuous discovery/status loop for a referenced
`Backend`:

- the Backend must exist and have `spec.discovery.enabled=true`;
- `AdapterPolicy.spec.adapter` must match `Backend.spec.adapter`, or the
  built-in adapter name derived from `Backend.spec.driver`;
- `AdapterPolicy.spec.selector` is ANDed with `Backend.spec.discovery.selector`
  before discovery reaches the adapter;
- `AdapterPolicy.spec.dryRun=true` validates the policy, Backend reference,
  adapter resolution, and merged selector without invoking adapter discovery;
- the controller resolves the backend driver through `adapter.Registry`;
- `Adapter.Discover` receives the Backend, driver, runtime, namespace
  parameter, selector, max object limit, and backend parameters;
- `AdapterPolicy.status.discoveredObjects` mirrors the latest aggregate object
  count for quick inspection. For built-in Argo CD and Flux discovery this
  mirrors the live `Backend.status` counts written by `BackendReconciler`; for
  other registered adapters it uses the adapter discovery result.

`BackendReconciler` remains the single writer for `Backend.status`.
`AdapterPolicy` records its own health and quick counts only. Full
public-adapter delivery wiring still needs migration from direct
actuator/spoke-provider registries where appropriate and conformance coverage
for out-of-tree adapter authors.
