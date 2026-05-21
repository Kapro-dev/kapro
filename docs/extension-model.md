# Kapro Extension Model

Kapro is a Kubernetes-native fleet promotion control plane. The core
controllers own promotion ordering, stage fan-out, retries, rollback intent, and
status. Extension points are narrow contracts around backend-specific work.

This document defines the target architecture for those contracts.

## Architecture Goals

- Keep the CRD API small and stable.
- Keep controller state in Kubernetes, not process memory.
- Let platform teams integrate delivery backends without changing the promotion
  state machine.
- Let teams add safety checks without turning Kapro into a CI workflow engine.
- Emit standard lifecycle events that external systems can consume without
  linking against Kapro code.
- Move to out-of-process plugins only at stable, well-defined boundaries.

## Extension Surfaces

| Surface | Contract | Responsibility | Status |
|---|---|---|---|
| Actuator | `pkg/actuator` | Apply one version to one target and report convergence. | In-process registry |
| Gate | `pkg/gate` | Decide whether one target may advance. | In-process registry |
| Template gate | CEL, Job, Webhook gate templates | Configure custom gate behavior through CRDs. | Implemented |
| PromotionRun planner | `pkg/planner` and KPI proto | Filter, score, reserve, and permit rollout targets before binding. | In-process framework; KPI API preview |
| Lifecycle events | CloudEvents webhook payloads | Publish PromotionRun, stage, gate, approval, and target events. | Implemented |
| Notifications | Inline gate/stage notification settings | Route gate events without adding a separate public notification API. | Runtime path |
| Plugin gateway | KAI/KGI/KPI proto contracts and `Plugin` | Register and probe out-of-process actuators, gates, and planner plugins. | Hot-loaded actuator, gate, and planner dispatch preview |
| Trigger | CRD API | Define safe autonomous Promotion creation/update policy. | OCI controller preview |

## Core Boundary

Kapro core is responsible for:

- resolving `Plan` stages and dependencies;
- selecting `Cluster` targets;
- creating and updating `Target` state;
- evaluating retries, timeouts, failure policy, and rollback intent;
- recording status and Kubernetes Events;
- emitting lifecycle notifications.

Extensions must not own those responsibilities. They receive a bounded request,
perform backend-specific work, and return a bounded result.

## Actuator Contract

An actuator applies a desired artifact version to a target cluster.

The contract is intentionally narrow:

```text
Apply(version, target) -> accepted or error
IsConverged(version, target) -> true, false, or error
Rollback(previousVersion, target) -> accepted or error
```

The actuator can patch a GitOps object, call an external delivery API, or update
a Kubernetes workload. Kapro does not interpret backend-specific rollout
strategy. The backend controller owns how the workload changes after Kapro
patches the version field.

Target actuator examples:

| Backend | Version mutation | Readiness signal |
|---|---|---|
| Flux pull | OCI source reference | Workload Ready condition |
| Flux push | ResourceSet input version | Workload Ready condition |
| Argo CD | Application or ApplicationSet revision | Synced and Healthy status |
| Kubernetes | Workload image reference | Workload Available condition |
| KServe | Model storage URI | InferenceService Ready condition |

## Gate Contract

A gate answers one question:

```text
May this target advance now?
```

Gate results are normalized:

| Result | Meaning |
|---|---|
| Passed | The target may advance. |
| Failed | The target must stop according to failure policy. |
| Running | The gate is still evaluating. |
| Inconclusive | The gate could not make a final decision yet. |

Gate state is persisted on `Target` status. A controller restart must not
lose gate progress.

Gate results may include structured evidence. Evidence records the observed
facts behind the decision, such as metric query, observed value, threshold,
baseline value, sample count, confidence, and reason. This keeps gate decisions
auditable and gives external systems a stable machine-readable contract without
making those systems authoritative for rollout state.

Gate categories:

| Category | Purpose |
|---|---|
| Verification | Validate artifact integrity and provenance. |
| Soak | Hold a target for a configured duration. |
| Metrics | Evaluate service health against telemetry. |
| Approval | Wait for a human approval object. |
| CEL | Evaluate lightweight policy expressions. |
| Job | Run a Kubernetes Job and read its result. |
| Webhook | Ask an external policy service for a result. |

## Lifecycle Events

Lifecycle events are Kapro's integration boundary for downstream systems.

Kapro emits semantic event types for:

- promotion started, completed, failed, and rollback started;
- stage completed;
- gate passed and failed;
- approval required;
- target phase changes.

Webhook notifications can use plain JSON or CloudEvents v1.0 structured JSON.
CloudEvents IDs are stable for a given PromotionRun, event type, `Plan` node,
stage, target, and phase, allowing receivers to de-duplicate retries.

Inline notifications on gate policies remain supported and are the active
runtime path today. Kapro does not expose separate public notification
provider/policy CRDs in the KISS API.

External consumers can implement audit trails, chat notifications, incident
routing, compliance ingestion, or repository dispatch without becoming Kapro
plugins.

## Plugin Gateway API Preview

The current execution registries are in-process. Kapro now defines the API
surface for out-of-process actuator, gate, and planner plugins:

- `spec/kai/v1alpha1/actuator.proto`
- `spec/kgi/v1alpha1/gate.proto`
- `spec/kpi/v1alpha1/planner.proto`
- `Plugin`

```text
Kapro controller
  -> PluginGateway
    -> external actuator plugin
    -> external gate plugin
    -> external planner plugin
```

Runtime registration through `Plugin` is an opt-in API preview.
When `KAPRO_ENABLE_PLUGIN_GATEWAY=true`, the operator loads ready registrations
with fresh observed generation into the actuator, gate, and planner registries.
Plugin changes are hot-loaded after readiness probes succeed; stale,
incompatible, and deleted plugins are unloaded.

API pieces:

| Piece | Purpose |
|---|---|
| KAI proto | Language-neutral actuator contract. |
| KGI proto | Language-neutral gate contract. |
| KPI proto | Language-neutral planner contract for filtering and ordering targets. |
| Plugin CRD | Declarative registration of external plugin endpoints. |
| Conformance harnesses | Base checks external plugin authors can run. |
| PluginGateway | Runtime boundary for enabled contracts, timeout handling, retries, and error normalization. |

The gateway must preserve the same state ownership rule: plugins do backend
work, Kapro owns PromotionRun state.

API maturity, deprecation rules, upgrade policy, and the future non-binding
certified plugin path are defined in `docs/api-stability.md`. KAI, KGI, and KPI
conformance instructions are defined in `docs/plugin-authoring.md`.

Creating or updating a `Plugin` is a platform-admin action. External plugins are
inside the delivery integration boundary, not inside Kapro's control-plane trust boundary.
They must not create or mutate Kapro PromotionRun state directly. See
`docs/security.md` and `docs/rbac-tenancy.md` for trust boundary, Secret
handling, RBAC, and tenancy rules.

Plugin readiness follows the compatibility matrix in
`docs/plugin-authoring.md`. Unsupported or missing KAI/KGI/KPI contract
versions are reported as `Ready=False` and `Compatible=False` on
`Plugin` status and are not loaded for runtime dispatch.

## Trigger Target

`Trigger` is the API boundary for autonomous Promotion creation and
updates. The CRD defines safe source observation and Promotion update policy.
The controller observes OCI registries and updates digest-pinned Promotion
intent after safeguards pass; the Promotion controller then stamps
`PromotionRun` attempts.

The safe flow is:

```text
artifact source -> Trigger -> suspended Promotion -> PromotionRun attempt -> Plan
```

Required safeguards:

| Safeguard | Required behavior |
|---|---|
| Suspended by default | Detection does not equal deployment. |
| Digest pinning | Promotions reference immutable artifact digests before attempts are stamped. |
| Signature verification | Unsigned artifacts do not update Promotion intent. |
| Tag filtering | Only configured patterns update Promotion intent. |
| Cooldown | Rapid artifact pushes cannot flood the fleet. |
| Max active | One trigger cannot create unlimited concurrent attempts. |
| Scope | Triggers can be limited to canary stages or selected targets. |
| Idempotency | Re-observed artifacts do not create duplicate attempts. |

Promotion trigger behavior is covered by the public API docs, release notes,
and ADRs under `docs/adr/`.

## PromotionRun Planner

The PromotionRun planner is the target-selection boundary inside `PromotionRunReconciler`.
It follows Kubernetes scheduler-style phases:

```text
PreFilter -> Filter -> Score -> NormalizeScore -> Reserve -> Permit -> bind Target
```

Kapro keeps ownership of PromotionRun state. Planner plugins can influence which
targets are eligible and in what order they are bound, but they do not create
or mutate `Target` objects directly.

Built-in planning behavior:

| Plugin or strategy | Phase | Behavior |
|---|---|---|
| Readiness filter | Filter | Skips targets that explicitly report `Ready=False`. |
| Active PromotionRun filter | Filter | Skips targets already processing a different PromotionRun. |
| Deterministic ordering | Score | Keeps stable name-based ordering when scores tie. |
| Stage strategy | Bind | Enforces `Stage.spec.strategy.maxParallel` before creating new `Target` entries. |

`Stage.status.plannerResults` records skip and defer reasons so operators can
see why a target was not bound in the current planning cycle. External planner
plugins can filter, defer, and score targets, but Kapro still owns
`Target` creation and PromotionRun state.

## CRD Rule

Add a CRD only when the concept has independent lifecycle, status, RBAC, or
cross-resource reuse. Keep configuration inline when it is only used by one
owning resource.

Target CRD posture:

| API surface | Posture |
|---|---|
| Existing `Promotion`, `Plan`, `Source`, unit, `Cluster`, `Target`, `Approval`, `Backend`, `Trigger`, and `Policy` CRDs | Core API |
| `Plugin` | API preview; opt-in hot-loaded runtime registration |
| `Trigger` | API preview with ADR-002 safeguards; OCI controller preview |
| Notification provider/policy | Add only when shared credential ownership requires it |
| Metric definition | Add only when metric reuse needs independent ownership |
| Gate template | Keep inline until it needs independent lifecycle |
