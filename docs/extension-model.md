# Kapro Extension Model

Kapro is a Kubernetes-native fleet promotion control plane. The core
controllers own release ordering, stage fan-out, retries, rollback intent, and
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
| Release planner | `pkg/planner` and KPI proto | Filter, score, reserve, and permit rollout targets before binding. | In-process framework; KPI API preview |
| Lifecycle events | CloudEvents webhook payloads | Publish release, stage, gate, approval, and target events. | Implemented |
| Plugin gateway | KAI/KGI/KPI proto contracts and `PluginRegistration` | Register and probe out-of-process actuators, gates, and planner plugins. | Startup-time actuator and gate dispatch preview; planner status preview |
| ReleaseTrigger | CRD API | Define safe autonomous Release creation policy. | OCI controller preview |

## Core Boundary

Kapro core is responsible for:

- resolving `Pipeline` stages and dependencies;
- selecting `MemberCluster` targets;
- creating and updating `ReleaseTarget` state;
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

Gate state is persisted on `ReleaseTarget` status. A controller restart must not
lose gate progress.

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

- release started, completed, failed, and rollback started;
- stage completed;
- gate passed and failed;
- approval required;
- target phase changes.

Webhook notifications can use plain JSON or CloudEvents v1.0 structured JSON.
CloudEvents IDs are stable for a given release, event type, pipeline, stage,
target, and phase, allowing receivers to de-duplicate retries.

External consumers can implement audit trails, chat notifications, incident
routing, compliance ingestion, or repository dispatch without becoming Kapro
plugins.

## Plugin Gateway API Preview

The current execution registries are in-process. Kapro now defines the API
surface for out-of-process actuator, gate, and planner plugins:

- `spec/kai/v1alpha1/actuator.proto`
- `spec/kgi/v1alpha1/gate.proto`
- `spec/kpi/v1alpha1/planner.proto`
- `PluginRegistration`

```text
Kapro controller
  -> PluginGateway
    -> external actuator plugin
    -> external gate plugin
    -> external planner plugin
```

Runtime registration through `PluginRegistration` is an opt-in API preview.
When `KAPRO_ENABLE_PLUGIN_GATEWAY=true`, the operator loads ready registrations
with fresh observed generation into the actuator and gate registries once at
startup. Planner registrations are probed and reported in status; runtime
planner dispatch remains future work. Dynamic hot reload is future work.

API pieces:

| Piece | Purpose |
|---|---|
| KAI proto | Language-neutral actuator contract. |
| KGI proto | Language-neutral gate contract. |
| KPI proto | Language-neutral planner contract for filtering and ordering targets. |
| PluginRegistration CRD | Declarative registration of external plugin endpoints. |
| Conformance harnesses | Base checks external plugin authors can run. |
| PluginGateway | Runtime boundary for enabled contracts, timeout handling, retries, and error normalization. |

The gateway must preserve the same state ownership rule: plugins do backend
work, Kapro owns release state.

API maturity, deprecation rules, upgrade policy, and the future non-binding
certified plugin path are defined in `docs/api-stability.md`. KAI, KGI, and KPI
conformance instructions are defined in `docs/conformance.md`.

## ReleaseTrigger Target

`ReleaseTrigger` is the API boundary for autonomous release creation. The CRD
defines safe source observation and Release creation policy. The controller
observes OCI registries and creates digest-pinned Releases after safeguards pass.

The safe flow is:

```text
artifact source -> ReleaseTrigger -> suspended Release -> normal Kapro pipeline
```

Required safeguards:

| Safeguard | Required behavior |
|---|---|
| Suspended by default | Detection does not equal deployment. |
| Digest pinning | Releases reference immutable artifact digests. |
| Signature verification | Unsigned artifacts do not create releases. |
| Tag filtering | Only configured patterns create releases. |
| Cooldown | Rapid artifact pushes cannot flood the fleet. |
| Max active | One trigger cannot create unlimited concurrent releases. |
| Scope | Triggers can be limited to canary stages or selected targets. |
| Idempotency | Re-observed artifacts do not create duplicate releases. |

See `docs/ADR-002-release-trigger.md` for the release trigger decision record.

## Release Planner

The release planner is the target-selection boundary inside `ReleaseReconciler`.
It follows Kubernetes scheduler-style phases:

```text
PreFilter -> Filter -> Score -> NormalizeScore -> Reserve -> Permit -> bind ReleaseTarget
```

Kapro keeps ownership of release state. Planner plugins can influence which
targets are eligible and in what order they are bound, but they do not create
or mutate `ReleaseTarget` objects directly.

Built-in planning behavior:

| Plugin or strategy | Phase | Behavior |
|---|---|---|
| Readiness filter | Filter | Skips targets that explicitly report `Ready=False`. |
| Active release filter | Filter | Skips targets already processing a different Release. |
| Deterministic ordering | Score | Keeps stable name-based ordering when scores tie. |
| Stage strategy | Bind | Enforces `Stage.spec.strategy.maxParallel` before creating new `ReleaseTarget` entries. |

`Stage.status.plannerResults` records skip and defer reasons so operators can
see why a target was not bound in the current planning cycle. External planner
runtime execution is future work; the KPI proto defines the contract first.

## CRD Rule

Add a CRD only when the concept has independent lifecycle, status, RBAC, or
cross-resource reuse. Keep configuration inline when it is only used by one
owning resource.

Target CRD posture:

| API surface | Posture |
|---|---|
| Existing release, pipeline, app, cluster, target, approval, and policy CRDs | Core API |
| `PluginRegistration` | API preview; opt-in startup-time runtime registration |
| `ReleaseTrigger` | API preview with ADR-002 safeguards; OCI controller preview |
| Notification provider/policy | Add only when shared credential ownership requires it |
| Metric definition | Add only when metric reuse needs independent ownership |
| Gate template | Keep inline until it needs independent lifecycle |
