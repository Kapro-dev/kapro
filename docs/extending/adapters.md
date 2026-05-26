# Adapters

Kapro exposes a public Go adapter package at
`kapro.io/kapro/pkg/kapro/adapter`.

The package is an SDK boundary for delivery substrates.
`SubstrateDiscoveryPolicy` discovery uses this public registry to resolve the
selected substrate kind and call `Adapter.Discover`. Delivery side effects still
use the existing actuator and spoke-provider paths until execution selection
moves to the public contract.

## Public Contract

An adapter owns substrate-specific delivery work for one substrate kind:

```go
type Adapter interface {
	SubstrateKind() v1alpha1.SubstrateKind
	ExecutionScope() v1alpha1.ExecutionScope
	Capabilities() adapter.Capabilities
	Apply(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Observe(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Rollback(ctx context.Context, req adapter.Request) (adapter.Result, error)
	Discover(ctx context.Context, req adapter.DiscoveryRequest) (adapter.DiscoveryResult, error)
}
```

`Capabilities` is the first thing controllers should read. It tells callers
which methods are meaningful for a substrate so unsupported operations do not
become normal error-path control flow. See
[Adapter Capabilities](adapter-capabilities.md) for each bit.

`Request` carries the PromotionRun identity, target cluster, selected Substrate,
delivery mode, app key, version, previous version, desired version map, and
merged substrate parameters.

`Result` normalizes delivery progress into `DeliveryPhase`, convergence,
changed count, digest, artifact format, applied object count, and optional
substrate object status evidence.

`DiscoveryRequest` and `DiscoveryResult` model the observe/adopt discovery
shape used by `Substrate.status.selectedObjects`, `skippedObjects`, and
`unsupportedPatterns`.

## Registry

`adapter.Registry` is thread-safe and resolves adapters by
`v1alpha1.SubstrateKind`:

```go
reg := adapter.NewRegistry()
_ = reg.Register(flux.New())

substrate, err := reg.Resolve(v1alpha1.SubstrateKindFlux)
```

`Register` fails on nil, empty substrate kind, or duplicate kind. `Upsert` is
available when replacement is intentional.

## Reference Adapters

Reference packages live under `pkg/kapro/adapter`:

| Package | Substrate kind | Execution scope | Current role |
|---|---|---|---|
| `adapter/argocd` | `argo` | `Hub` | Models Argo CD discovery. |
| `adapter/flux` | `flux` | `Both` | Models Flux discovery. |
| `adapter/oci` | `oci` | `Spoke` | Reports discovery unsupported because OCI delivery has no substrate-native Kubernetes objects. |

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

## SubstrateDiscoveryPolicy Discovery

`SubstrateDiscoveryPolicy` runs a continuous discovery/status loop for a referenced
`Substrate`:

- the Substrate must exist and have an active `spec.discovery` block
  (`spec.discovery.suspended` omitted or `false`);
- `SubstrateDiscoveryPolicy.spec.expectedKind`, when set, must match
  `Substrate.spec.classRef.name`;
- `SubstrateDiscoveryPolicy.spec.selector` is ANDed with `Substrate.spec.discovery.selector`
  before discovery reaches the adapter;
- `SubstrateDiscoveryPolicy.spec.dryRun=true` validates the policy, Substrate reference,
  adapter resolution, and merged selector without invoking adapter discovery;
- the controller resolves the substrate kind through `adapter.Registry`;
- `Adapter.Discover` receives the Substrate, substrate kind, execution scope, namespace
  parameter, selector, max object limit, and substrate parameters;
- `SubstrateDiscoveryPolicy.status.discoveredObjects` mirrors the latest aggregate object
  count for quick inspection. For built-in Argo CD and Flux discovery this
  mirrors the live `Substrate.status` counts written by `SubstrateReconciler`; for
  other registered adapters it uses the adapter discovery result.

`SubstrateReconciler` remains the single writer for `Substrate.status`.
`SubstrateDiscoveryPolicy` records its own health and quick counts only. Full
public-adapter delivery wiring still needs migration from direct
actuator/spoke-provider registries where appropriate and conformance coverage
for out-of-tree adapter authors.
