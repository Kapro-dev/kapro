# Kapro Substrate Parameter Spec v1alpha1

This document defines the contract for typed substrate configuration in Kapro.
It is a public extension contract, not an ADR. ADRs explain why the project
chose this model; this spec explains what conformant substrate authors must
implement.

## Goals

Kapro core must stay substrate-neutral. It should not import Argo CD, Flux,
Helm, KServe, webhook, Terraform, or platform-specific API types. A substrate
implementation owns those details through typed CRDs and a KSI implementation.

The v1alpha1 contract introduces the config side first:

- `SubstrateClass`: cluster-scoped class and controller binding. The
  `substrateclass` controller writes status for Kapro-owned classes; external
  substrate controllers write status for their own `controllerName`.
- `Substrate.spec.classRef`: selects a `SubstrateClass`.
- `Substrate.spec.configRef`: points at a typed substrate config object.
- `Substrate.spec.parameters`: retained as a compatibility and demo escape hatch.
- `delivery.parameters`: retained as the app-level binding surface until typed
  binding CRDs are introduced.

Typed binding CRDs and `delivery.bindingRef` are intentionally deferred until
the config contract has proved stable.

## Core Shapes

### SubstrateClass

`SubstrateClass` declares the class controller identity and reports accepted
typed config kinds and capabilities.

```yaml
apiVersion: kapro.io/v1alpha1
kind: SubstrateClass
metadata:
  name: argo
  labels:
    kapro.io/family: gitops
    kapro.io/ledger: git
spec:
  controllerName: kapro.io/argo
  executionModes:
    default: hub-push
status:
  executionModes:
    supported:
      - hub-push
  acceptedConfigKinds:
    - apiVersion: argocd.substrate.kapro.io/v1alpha1
      kind: ArgoCDSubstrateConfig
  capabilities:
    operations:
      apply: true
      observe: true
      dryRun: true
      rollback: false
      discover: true
    staging:
      twoPhase: false
    inputTypes:
      - git-revision
      - helm-chart
  conditions:
    - type: Accepted
      status: "True"
```

`spec.controllerName` follows the Gateway API controller-name convention: it is
a domain-prefixed path that identifies the controller responsible for this
class.

`status.executionModes.supported`, `status.acceptedConfigKinds`, and
`status.capabilities` are controller-reported truth. They are status fields
because admins should not be able to declare support the implementation does
not actually have.

### Substrate Config Selection

`Substrate` remains the configured delivery instance in v1alpha1. New substrates
should use `classRef` plus `configRef`.

```yaml
apiVersion: kapro.io/v1alpha1
kind: Substrate
metadata:
  name: prod-argo
spec:
  classRef:
    name: argo
  configRef:
    apiVersion: argocd.substrate.kapro.io/v1alpha1
    kind: ArgoCDSubstrateConfig
    name: prod-argo
  execution:
    mode: hub-push
```

`Substrate.spec.substrate` and `Substrate.spec.execution` are the open,
non-typed path. The old prototype `driver`, `adapter`, and `runtime` fields are
not part of the 0.6 public-preview CRD.

## Typed Config CRDs

Each substrate package owns a typed config CRD. The config CRD stores platform
wiring: endpoint, credentials, namespace, defaults, and operational timeouts.

When a substrate config needs credentials, it must use `spec.credentialsRef`
with Kubernetes `SecretReference` semantics. When it connects to a network
endpoint, it must use `spec.endpoint` or document why no endpoint is required.
Substrates that do not need credentials or an endpoint may omit those fields.

Reference config examples:

```yaml
apiVersion: argocd.substrate.kapro.io/v1alpha1
kind: ArgoCDSubstrateConfig
metadata:
  name: prod-argo
spec:
  endpoint: https://argo.example.com
  namespace: argocd
  credentialsRef:
    name: prod-argo-token
    namespace: kapro-system
  defaultProject: platform
```

```yaml
apiVersion: flux.substrate.kapro.io/v1alpha1
kind: FluxSubstrateConfig
metadata:
  name: prod-flux
spec:
  namespace: flux-system
```

```yaml
apiVersion: kubernetes.substrate.kapro.io/v1alpha1
kind: KubernetesApplyConfig
metadata:
  name: local-direct
spec:
  serverSideApply: true
  fieldManager: kapro
  namespace: default
```

```yaml
apiVersion: oci.substrate.kapro.io/v1alpha1
kind: OCIBundleApplyConfig
metadata:
  name: prod-oci
spec:
  serverSideApply: true
  fieldManager: kapro
  registryCredentialsRef:
    name: prod-registry
    namespace: kapro-system
```

## App Binding Phase

For v1alpha1, app/workload mapping remains in `delivery.parameters`:

```yaml
delivery:
  substrateRef: prod-argo
  parameters:
    application: payments-prod
    versionField: spec.source.targetRevision
```

`versionField` is a common convention, not a universal Kapro requirement.
Kapro core passes desired versions to KSI. The substrate implementation maps
those versions to native fields, templates, manifests, or API calls.

Future typed binding CRDs will move this app-level shape out of
`delivery.parameters`. At that point, Kapro will add the binding reference
field and class status for accepted binding kinds in the same release:

```yaml
delivery:
  substrateRef: prod-argo
  bindingRef:
    apiVersion: argocd.substrate.kapro.io/v1alpha1
    kind: ArgoCDApplicationBinding
    name: payments-prod
```

## KSI Contract

Substrate implementations expose the Kapro Substrate Interface (KSI):

```go
type Substrate interface {
    Validate(ctx context.Context, req *ValidateRequest) (*ValidateResult, error)
    Apply(ctx context.Context, req *ApplyRequest) (*ApplyResult, error)
    Observe(ctx context.Context, req *ObserveRequest) (*ObserveResult, error)
    Capabilities(ctx context.Context) (*Capabilities, error)
}
```

KSI requests carry the resolved `SubstrateClass`, `Substrate`, typed `Config`,
target `Cluster`, desired versions, and compatibility parameters. The typed
`Binding` field is nil in v1alpha1 Phase 1.

Optional extensions are advertised through capabilities and Go type assertion:

- `Rollbacker`
- `TwoPhaser`
- `Discoverer`

KSI implementations must be idempotent for repeated `Apply` calls with the
same request identity and desired versions.

### KSI, KSP, And Legacy Actuators In 0.6

KSI is the public substrate author contract. Third-party delivery integrations
should target `pkg/kapro/substrate.Substrate`, typed config CRDs, and this
parameter spec.

KSP (`pkg/spokeprovider.Provider`) is the spoke-side provider contract used by
`kapro-cluster-controller` when a substrate must execute near the target
cluster. Implement KSI when authoring a substrate package, class, typed config,
and hub-side conformance contract. Implement KSP only when that substrate also
needs spoke-side pull delivery or observation. A substrate may have both: KSI
defines the public class/config and request/result surface, while KSP performs
local reconcile ticks for the selected `SubstrateKind`.

Some in-tree `0.6` runtime paths still execute through Kapro's older
`pkg/kapro/actuator.Actuator` interface while they are being bridged into KSI.
That legacy actuator layer is an internal runtime adapter for existing hub and
spoke delivery code; it is not the preferred extension point for new substrate
authors. New spoke-pull substrates should use KSP instead of adding another
actuator shape.

The `0.6.0` conformance gate therefore has three parts:

- KSI reference class scenarios for `kubernetes-apply`, `argo`, `flux`, and `oci`
  prove the public request/result contract.
- KSP provider conformance proves spoke-side provider behavior for substrate
  implementations that execute from `kapro-cluster-controller`.
- Runtime actuator/controller tests prove the current in-tree direct, Argo CD,
  Flux, and OCI delivery paths while those bridges remain in place.

Before KSI is promoted beyond alpha, each launch substrate should either expose
a native KSI implementation or an explicit, tested bridge from the legacy
actuator path into KSI.

## Status Contract

Substrate controllers should write these core `Substrate.status.conditions`
where applicable:

| Condition | Meaning |
| --- | --- |
| `Ready` | Overall configured substrate health. |
| `ClassAccepted` | `Substrate.spec.classRef` resolved to an accepted class. |
| `ConfigAccepted` | `Substrate.spec.configRef` resolved and matched an accepted config kind. |
| `Reachable` | The substrate endpoint or Kubernetes API path is reachable. |
| `Authorized` | Credentials were validated or auth is not required. |

Substrate-specific status belongs on the typed config CRD, not in Kapro core
status. For example, an Argo CD config can report API health, while a future
Argo CD binding can report Application sync and health state.

## Lifecycle Rules

- Deleting a `Substrate` must not cascade-delete substrate-native resources.
  Orphan is the v1alpha1 default.
- Deleting a `SubstrateClass` with referencing Substrates must result in
  `ClassAccepted=False` on those Substrates.
- Kapro core reads typed config through Kubernetes dynamic clients. It must not
  import substrate-specific Go packages.
- Substrate packages own their config CRDs, validation, controllers, docs, and
  conformance scenarios.

## Naming Conventions

- Config API group: `<substrate>.substrate.kapro.io`.
- Config kind suffix: `SubstrateConfig` or a precise substrate name such as
  `KubernetesApplyConfig`.
- Class name: kebab-case package name such as `argo`, `kubernetes-apply`,
  or `oci`.
- Controller name: domain-prefixed path such as `kapro.io/argo`.

## Conformance

A conformant substrate should pass tests that verify:

- declared config kinds are installed and accepted by `SubstrateClass.status`;
- mandatory config fields use the standard names when applicable;
- KSI methods exist and are idempotent;
- capability bits match optional interfaces;
- missing config, missing credentials, and invalid credentials surface
  deterministic conditions instead of panics;
- `Apply` followed by `Observe` reaches a deterministic terminal or retryable
  state.

The conformance suite is the enforcement mechanism for this spec. The first
`0.6.0` reference classes are intentionally `kubernetes-apply`, `argo`, `flux`,
and `oci`: the first three cover direct apply and GitOps adoption, while OCI
proves artifact-backed Gitless delivery without becoming a default dependency.
The suite may start as an internal Go test contract; a public
`kapro substrate conformance <class>` CLI is later `0.7.x` work. Webhook,
pipeline, platform, and custom API classes remain valid extension families once
they can pass the same contract.
