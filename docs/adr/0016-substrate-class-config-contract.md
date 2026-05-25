# ADR-0016: Substrate Class And Typed Config Contract

## Status
Superseded in part by [ADR-0018](0018-public-runtime-api-split.md)

ADR-0018 completes the rename from `Backend` to `Substrate` and resets the
public-preview API to `kapro.io/v1alpha1`. The class/config contract below
remains the basis for 0.6 substrates; `Backend` references are historical.

## Context

Kapro started with `Backend.spec.driver`, `adapter`, and `runtime`, then added
`Backend.spec.substrate` and `execution` as an open substrate shape. That
solved the closed-enum problem but still left two architectural gaps:

- real substrates need different typed configuration, credentials, defaults,
  validation, and controller behavior;
- Kapro needs a public ecosystem contract for GitOps and non-GitOps delivery
  without forcing core controllers to import Argo CD, Flux, Helm, KServe, or
  platform-specific API packages.

The Kubernetes ecosystem has converged on class resources and
implementation-specific parameter resources for this problem. StorageClass,
IngressClass, GatewayClass, RuntimeClass, Crossplane ProviderConfig, CSI, and
CNI all separate the common selection contract from implementation-specific
details.

## Decision

We introduce a Phase-1 substrate contract:

- `SubstrateClass` is a cluster-scoped Kapro CRD that declares
  `spec.controllerName` and reports accepted config kinds, supported execution
  modes, and capabilities in status.
- `Backend` remains the configured delivery instance in v1alpha2. It gains
  `spec.classRef` and `spec.configRef` while retaining the open
  `spec.substrate`, `spec.execution`, and `spec.parameters` fields.
- Each substrate package owns its typed config CRD, for example
  `ArgoCDSubstrateConfig`, `FluxSubstrateConfig`, `KubernetesApplyConfig`,
  or `OCIBundleApplyConfig`.
- The `substrateclass` controller writes status for Kapro-owned classes
  (`kapro.io/*` controller names). External substrate controllers own status
  for their own domain-prefixed controller names.
- App/workload-specific binding remains in `substrate.parameters` for Phase 1.
  Typed binding CRDs and `delivery.bindingRef` are deferred until the config
  contract is proven.
- KSI, the Kapro Substrate Interface, is the public Go contract for substrate
  validation, apply, observe, capabilities, and optional rollback/staging/
  discovery extensions.
- The external substrate author contract is documented in
  `docs/specs/substrate-parameter-spec.md`.
- During the `0.6` migration, some in-tree runtime adapters still use the
  older `pkg/kapro/actuator.Actuator` interface. KSI is the public substrate
  contract; the legacy actuator layer is an internal compatibility path until
  the launch substrates are ported or bridged.

## Rejected alternatives

### Keep only `Backend.spec.substrate.kind` and `actuator`

This keeps manifests short but cannot express typed substrate configuration.
Argo CD, direct Kubernetes apply, webhook, KServe, Helm, and internal platforms
need different credential, endpoint, timeout, and defaulting rules. A string map
would push errors to runtime and make conformance weak.

### Add `SubstrateConfig` as a third core CRD

Kapro already has an app intent layer through Fleet, Cluster, Promotion, Plan,
and Source objects. Adding a generic `SubstrateConfig` CRD would create another
Kapro-owned abstraction while still failing to type substrate-specific fields.
Typed config CRDs owned by substrate packages are clearer.

### Rename `Backend` to `Substrate` immediately

The repository already has `Backend` references in `Fleet`, `Cluster`,
`Source`, `SubstrateDiscoveryPolicy`, examples, docs, admission, and runtime controllers.
Renaming now would be a large migration with little immediate runtime value.
The Phase-1 contract is additive and leaves the rename decision for a later API
transition.

### Add typed binding CRDs in the same PR

The end state is config plus binding: platform-owned config and app-owned
binding. Shipping both immediately would add too many CRDs and force new users
through more objects before the class/config contract is proven. Phase 1 keeps
`substrate.parameters` as the binding-equivalent compatibility surface.

### Require `versionField` in every binding

`versionField` works well for Argo CD and other field-writer substrates, but it
does not fit every substrate. Direct apply may render a manifest set, webhook
may template a body, and model-serving substrates may use a model URI mapping.
KSI passes desired versions; substrate implementations decide how those
versions map to native objects.

## Consequences

Kapro gets a clearer ecosystem path:

- core APIs stay small and substrate-neutral;
- platform teams can install new substrate packages without changing Kapro
  core;
- reference substrates can prove Gitless direct apply, Gitful Argo CD/Flux,
  OCI bundle delivery, and external webhook delivery;
- conformance can test the contract instead of asserting string-map
  conventions.

This also means the migration has stages:

1. add `SubstrateClass`, `Backend.spec.classRef`, `Backend.spec.configRef`, KSI,
   typed config CRDs, docs, and conformance scaffolding;
2. add typed binding CRDs and `delivery.bindingRef`;
3. decide whether `Backend` should become `Substrate` in a future API version.

## References

- `docs/specs/substrate-parameter-spec.md`
- ADR-0013: Go SDK versioning policy
- Kubernetes StorageClass
- Kubernetes IngressClass
- Kubernetes Gateway API GatewayClass
- Crossplane ProviderConfig
- containerd Runtime v2
