# Kapro — Product & Architecture Spec

> **Living document — current state only.** This file describes what is implemented and shipped.
> Planned work lives in `docs/ROADMAP.md`. Nothing is added here until the code is merged and tests pass.
> Always read this before starting any new feature work.

---

## Table of Contents

1. [Vision](#1-vision)
2. [Problem Statement](#2-problem-statement)
3. [Boundaries](#3-boundaries)
4. [Core Mental Model](#4-core-mental-model)
5. [Terminology](#5-terminology)
6. [CRD Inventory](#6-crd-inventory)
7. [Architecture](#7-architecture)
8. [Extension Interfaces](#8-extension-interfaces)
9. [Gate System](#9-gate-system)
10. [Actuator System](#10-actuator-system)
11. [Fleet Onboarding](#11-fleet-onboarding)
12. [Target FSM](#12-target-fsm)
13. [Approval Flow](#13-approval-flow)
14. [Notification & Events](#14-notification--events)
15. [PluginRegistration API Preview](#15-pluginregistration-api-preview)
16. [PromotionTrigger API Preview](#16-promotiontrigger-api-preview)
17. [Non-Goals](#17-non-goals)

---

## 1. Vision

Kapro is a **Kubernetes-native progressive delivery orchestrator for cluster fleets**.

It answers one question deterministically: *"Which clusters are allowed to receive this artifact version now, and why?"*

Kapro does not replace GitOps tools (Flux, Argo CD). It coordinates them —
sitting above delivery systems as the fleet promotion control plane that decides
*when* and *in what order* clusters receive new versions, based on Promotion
intent, PromotionRun attempts, target health, planning rules, gate evidence,
and approvals.

**Analogy:** Kubernetes is to containers what Kapro is to delivery waves. Kubernetes manages the lifecycle of containers on nodes. Kapro manages the lifecycle of promotions across target clusters in a fleet.

---

## 2. Problem Statement

Platform engineering teams running 10–500 Kubernetes clusters have three bad options today:

- **Manual**: hand-deploy between clusters with no guardrails, no audit trail, no rollback.
- **Bespoke**: per-team promotion plans in CI (GitHub Actions, Tekton, Jenkins) with hard-coded targets, no reusable gate logic, brittle failure modes.
- **Partial**: Argo Rollouts handles in-cluster canary but not cross-cluster waves. Flux applies GitOps but not the "should I apply?" decision.

No Kubernetes-native tool owns the **fleet promotion layer**: artifact version →
gates → multi-target wave → backend convergence → auditable Promotion outcome
across many clusters.

---

## 3. Boundaries

Kapro owns cross-cluster promotion state and decisions. It delegates work that
other cloud-native systems already own.

| Area | Kapro owns | Delegated to |
|---|---|---|
| Artifact movement | Promotion, PromotionRun, PromotionPlan, Stage, target state | CI systems and artifact builders |
| Fleet ordering | Waves, planning, concurrency, target binding | Not delegated |
| Workload rollout | Version intent and convergence checks | Flux, Argo CD, Kubernetes, or backend controllers |
| Traffic shifting | Gate on rollout result | Argo Rollouts, Flagger, service mesh, ingress controllers |
| Safety controls | Gate lifecycle, evidence, approval state | Domain-specific plugins and policy services |
| Automation | Guarded Promotion creation; controller-owned PromotionRun attempts | PromotionTrigger policy and external CI/webhooks |
| Agents | Evidence explanation and policy-bound assistance | Never required for core deterministic rollout |

See `docs/vision-and-boundaries.md` for the public positioning and project
scope.

---

## 4. Core Mental Model

### Pod analogy

| Kubernetes | Kapro |
|---|---|
| `Node` | `FleetCluster` — a cluster registered in the fleet |
| `Pod` | (the per-target rollout tracked inline in `PromotionRun.status.targets[]`) |
| `Container` | Workload running inside a cluster |
| `PodSpec.nodeSelector` | `Stage.clusterSelector` — which clusters a stage targets |
| `Job` | `PromotionRun` — one execution attempt, terminates on completion |
| `CronJob` | `PromotionTrigger` — watches artifact sources and updates Promotion intent |

### Promotion execution DAG

```
PromotionRun
└── PromotionPlan DAG (PromotionPlan -> PromotionPlan via dependsOn)
    └── Stage DAG (Stage -> Stage via dependsOn inside a PromotionPlan)
        └── clusterSelector -> FleetCluster set -> child PromotionTarget per cluster/stage
```

- **Promotion** is the user-facing durable intent: version, scope, Kapro fleet,
  and rollout inputs.
- **PromotionRun** is the controller-owned execution attempt stamped from a
  Promotion and owns one terminal outcome.
- **PromotionPlan** is a reusable template of stages (no status, no live fields).
- **Stage** selects clusters and carries the gate policy that applies to those clusters.
- **PromotionSource** contains `PromotionUnit` mappings: the deployable units and
  the backend-native fields or files Kapro is allowed to change.
- **BackendProfile** selects the backend driver (`flux`, `argo`, `external`,
  etc.) and records discovery evidence for brownfield adoption.
- **FleetCluster** is inventory + delivery configuration + observed state.
- **PromotionTarget** is the authoritative per-cluster/stage execution object. `PromotionRun.status.targets[]` is retained only as deprecated compatibility state.
- **Approval** is a separate CRD that exists only to carry a human "approve / reject" signal for a target awaiting approval.

### What Kapro does **not** have

- No `Sync` CRD. Per-target execution state is materialized as child `PromotionTarget` objects and summarized back into `PromotionRun.status`.
- No `PromotionRunReport` / `PromotionRunRevision` CRD. Report summary is inline in `PromotionRun.status.report`; audit trail is inline in `PromotionRun.status.auditTrail`.
- No generic cluster-provider interface. `FleetCluster` is the single cluster CRD; bootstrap lives in code (`internal/bootstrap`).

---

## 5. Terminology

| Term | Meaning |
|------|---------|
| **Promotion** | User-facing durable intent for moving a version through one or more PromotionPlans. |
| **PromotionRun** | Controller-owned execution attempt stamped from a Promotion. |
| **Artifact** | Optional image, tag, digest, repository, or version metadata attached to a Promotion or stamped PromotionRun. |
| **PromotionSource** | Declares deployable PromotionUnits and their backend-native write targets. |
| **PromotionUnit** | One application, HelmRelease, Argo Application, ApplicationSet input, Git file field, or generated unit Kapro can promote. |
| **BackendProfile** | Selectable delivery backend profile for Flux, Argo, or external plugin-backed drivers. |
| **PromotionPlan** | Template only. Contains a DAG of `Stage`s. No status. |
| **Stage** | One wave inside a PromotionPlan. Selects target clusters via `clusterSelector` labels. Carries a `gate` policy. |
| **FleetCluster** | One workload cluster in the fleet. Holds actuator config, health check config, topology, and observed status (heartbeat + current versions). |
| **Approval** | A CRD carrying a human approve/reject signal for a `(PromotionRun, target)` pair. Deterministic name: `<promotionrun>-<target>`. |
| **Target** | A `FleetCluster` selected by a stage — i.e. one row in `PromotionRun.status.targets[]`. |
| **Gate** | One evaluation step in the target FSM. Returns `Passed`, `Failed`, `Running`, or `Inconclusive`. Stateless. |
| **Actuator** | Driver that applies a version to a target cluster through Flux, Argo, Kubernetes, or an external plugin. |

---

## 6. CRD Inventory

| CRD | Kind | Ownership | Scope |
|-----|------|-----------|-------|
| `kaproes.kapro.io` | `Kapro` | Platform | Cluster |
| `promotionsources.kapro.io` | `PromotionSource` | Platform | Cluster |
| `promotionplans.kapro.io` | `PromotionPlan` | Platform | Cluster |
| `promotions.kapro.io` | `Promotion` | Promotion engineer / automation | Cluster |
| `promotionruns.kapro.io` | `PromotionRun` | Controller | Cluster |
| `promotiontriggers.kapro.io` | `PromotionTrigger` | Platform / automation | Cluster |
| `promotiontargets.kapro.io` | `PromotionTarget` | Controller | Cluster |
| `backendprofiles.kapro.io` | `BackendProfile` | Platform | Cluster |
| `fleetclusters.kapro.io` | `FleetCluster` | Platform | Cluster |
| `pluginregistrations.kapro.io` | `PluginRegistration` | Platform | Cluster |
| `approvals.kapro.io` | `Approval` | Human via webhook | Cluster |
| `agentpolicies.kapro.io` | `AgentPolicy` | Platform | Cluster |

`PromotionSource`, `PromotionPlan`, and `BackendProfile` are reusable
configuration objects. User intent lives in `Promotion`; execution state lives in `PromotionRun`,
`PromotionTarget`, `FleetCluster`, `Approval`, and `PromotionTrigger` status.

The stable product center is the promotion execution path: `Promotion`,
`PromotionRun`, `PromotionTarget`, `PromotionPlan`, `FleetCluster`, `BackendProfile`,
`PromotionSource`, and `Approval`. Preview surfaces are documented separately:
`AgentPolicy` and the Decision API are opt-in assistance surfaces, and
unsupported `FleetClusterTemplate` import sources are not runtime features until
their controllers are implemented.

---

## 7. Architecture

```
┌────────────────────────────── Hub cluster ──────────────────────────────┐
│                                                                         │
│  ┌───────────────────────── kapro-operator ───────────────────────┐     │
│  │                                                                │     │
│  │  PromotionRunReconciler ─── drives the PromotionPlan DAG, owns  │     │
│  │                           the per-target FSM, resolves gates    │     │
│  │                           via gate.Registry                     │     │
│  │                                                                │     │
│  │  ApprovalReconciler    ─── watches Approval objects, unblocks  │     │
│  │                            targets in WaitingApproval          │     │
│  │                                                                │     │
│  │  CSRApprovalReconciler ─── approves spoke-cluster CSRs based   │     │
│  │                            on BootstrapToken                   │     │
│  │                                                                │     │
│  │  Admission webhooks    ─── FleetCluster + Approval validators │     │
│  └────────────────────────────────────────────────────────────────┘     │
│                                                                         │
│  ┌────────────────────── kapro-webhook-server ───────────────────┐      │
│  │  POST /approve/:token  → creates Approval(promotion-target)     │      │
│  │  POST /reject/:token   → creates Approval(promotion-target)     │      │
│  │  GET  /status/:promotionrun → returns PromotionRun.status               │      │
│  └───────────────────────────────────────────────────────────────┘      │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
                                   │ outbound
                                   ▼
┌─────────────────────── Spoke cluster (one per FleetCluster) ─────────────┐
│                                                                         │
│  kapro-cluster-controller                                               │
│    - watches hub intent for pull-mode delivery                          │
│    - patches the local delivery system when a spoke actuator is used     │
│    - renews Lease kapro-heartbeat-<cluster> in kapro-system             │
│    - writes FleetCluster.status (currentVersions + health summary)     │
│    - outbound-only HTTPS; authenticates via CSR-issued cert or GCP WIF  │
└─────────────────────────────────────────────────────────────────────────┘
```

Controllers are registered from `pkg/controllermanager/controllers.go`. Hub and spoke share the generated clientset but run different reconciler sets.

### Hub config source of truth

For the current public pre-stable line, hub configuration is sourced from a dedicated git repository and
applied to the hub cluster by CI with `kubectl apply`. The repository owns
`FleetCluster`, `BackendProfile`, `PromotionSource`, `PromotionPlan`, and
`Promotion` intent YAML. Built-in Argo and Flux adoption can
also write backend-native Git fields by creating a GitOps pull request or local
repository mutation, depending on the actuator configuration. Spoke clusters do
not watch the hub repository directly; they either consume their existing
GitOps backend or report status through the hub.

See `docs/hub-config-source-of-truth.md` and `examples/hub-config/`.

---

## 8. Extension Interfaces

Kapro has narrow pluggable interfaces for backend execution, safety evaluation,
and rollout planning. Actuators and gates are named runtime-resolved dispatch
points with conformance suites. The PromotionRun planner is an in-process framework
for target selection and ordering, modeled after Kubernetes scheduler phases.

| Interface | Go package | Question it answers | Conformance |
|-----------|------------|---------------------|-------------|
| Actuator (KAI) | `pkg/actuator` | "Apply this version to this cluster" | `conformance/actuator` |
| Gate (KGI)     | `pkg/gate`     | "May this target advance?"           | `conformance/gate`     |
| Planner (KPI) | `pkg/planner` | "Which targets should this stage bind, and in what order?" | `conformance/planner` |

Other internal concerns — health checking (`internal/health`), OCI fetch (`internal/oci/oras`), cosign verification (`internal/verification/cosign`), notification (`internal/notification`) — are **not** runtime extension points today. They live as internal packages with fixed implementations.

See `docs/extension-model.md` for the full extension boundary model and the
criteria for adding future plugin or CRD surfaces.
See `docs/api-stability.md` for API maturity, deprecation, and upgrade policy.
See `docs/conformance.md` for KAI, KGI, and KPI conformance instructions.

There is **no** cluster-provider interface. Cluster onboarding is concrete, not pluggable (see §10).

---

## 9. Gate System

A `Gate` (`pkg/gate.Gate`) is a stateless evaluator:

```go
type Gate interface {
    Evaluate(ctx context.Context, req Request) (Result, error)
}
```

`Result.Phase` is one of `Passed | Failed | Running | Inconclusive`. All timing, retry, and failure policy live in the `PromotionRunReconciler` — the gate just evaluates and returns.

Gate decisions are evidence-based:

```text
Evidence -> Analysis -> Phase
```

`Result.Evidence` and `status.targets[i].gates[].evidence[]` carry structured,
non-secret facts behind the decision: observed value, threshold, query, window,
sample count, confidence, p-value, effect size, score, baseline health,
baseline value, decision rule, and reason. Evidence is for audit, debugging,
notifications, and external agents. Gates must not store tokens, headers,
secrets, or raw webhook payloads in evidence.

All gate state for a running target is persisted on `PromotionRun.status.targets[i].gates[]` in etcd. Controller restarts do not lose gate progress.

Metric gates support explicit analysis modes:

| Mode | Purpose |
|------|---------|
| `threshold` | Current behavior. Compare one Prometheus instant value to a threshold. |
| `sloBurnRate` | Treat the value as error-budget burn rate and pass when it stays within budget. |
| `baseline` | Compare current/canary value to a baseline query as a ratio. |
| `sequential` | Query a range over the gate window and require minimum samples plus confidence before deciding. |
| `changePoint` | Compare early and late samples in the gate window to detect a significant regression. |
| `score` | Convert a metric into a 0-100 canary score and require a minimum score. |

Statistical modes are opt-in. If data is insufficient, Kapro returns
`Inconclusive` rather than passing.

### Registry

A single `gate.Registry` is built at startup (`BuildGateRegistry` in `pkg/controllermanager/controllers.go`) and registers every gate by name:

| Gate name       | Type               | Activation                                   |
|-----------------|--------------------|----------------------------------------------|
| `soak`          | built-in           | `Stage.gate.soakTime` is set                 |
| `metrics`       | built-in           | `Stage.gate.metrics` has queries             |
| `approval`      | built-in           | `Stage.approval.required` is true            |
| `verification`  | built-in           | `Stage.gate.verification` is configured      |
| `cel`           | template-dispatch  | `GateTemplate` with `spec.cel`               |
| `job`           | template-dispatch  | `GateTemplate` with `spec.job`               |
| `webhook`       | template-dispatch  | `GateTemplate` with `spec.webhook`           |

The built-ins and template-dispatch gates live in the **same registry**; there is no separate `GateSet`.

### FSM handlers

The target FSM (`internal/controller/target_fsm.go`) resolves gates by name via `r.GateRegistry.Resolve(<name>)` in each phase handler. Unknown gate names are a hard error, not a silent skip.

---

## 10. Actuator System

An `Actuator` (`pkg/actuator.Actuator`) drives the "apply this version to this cluster" step:

```go
type Actuator interface {
    Apply(ctx context.Context, req ApplyRequest) error
    IsConverged(ctx context.Context, cluster *FleetCluster, version, appKey string) (bool, error)
    Rollback(ctx context.Context, cluster *FleetCluster, previousVersion, appKey string) error
    ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error)
    IsAllConverged(ctx context.Context, cluster *FleetCluster, desiredVersions map[string]string) (bool, error)
}
```

Built-in backend adapters cover OCI pull desired state plus Flux and Argo CD
greenfield and brownfield shapes:

- OCI pull mode records desired versions on `FleetCluster` for outbound-only
  spoke clusters to apply locally;
- greenfield Flux generation can use Kapro-owned source and workload objects;
- brownfield Flux Git-native promotion updates existing `GitRepository`,
  `OCIRepository`, `HelmRelease`, Kustomize image, and chart version fields;
- brownfield Argo promotion updates existing `Application`, multi-source
  `Application`, `ApplicationSet`, file-generator, and app-of-apps patterns;
- external actuator plugins are selected through `BackendProfile.driver=external`
  and a ready `PluginRegistration`.

`PromotionSource.spec.units[]` is the write contract. Each `PromotionUnit`
declares the backend object, file path, and version field Kapro may update.
`BackendProfile` selects the driver and records discovery evidence, while
`FleetCluster` selects delivery configuration and reports observed versions.

Other actuators can delegate to Argo Rollouts, Flagger, Helm, KServe, Pulumi,
raw Kubernetes apply, Istio, Gateway API, or platform-specific systems through
the same contract. Kapro does not own those systems' in-cluster traffic shifting
or rollout algorithms; it gates and sequences promotion around their status.

The in-process actuator contract is a Preview surface; see
`docs/api-stability.md` before depending on it across minor releases.

---

## 11. Fleet Onboarding

Spoke onboarding is **not** a pluggable extension point. It uses two concrete paths:

1. **CSR-based (default).** The spoke cluster-controller submits a Kubernetes `CertificateSigningRequest` with a bootstrap token. The hub's `CSRApprovalReconciler` validates the token and approves the CSR. The spoke then receives a client certificate bound to a `system:kapro:<cluster>` identity.
2. **GCP Workload Identity Federation (optional).** When `FleetCluster.spec.bootstrap.gcp` is set, the spoke exchanges a GCP ID token for hub credentials via `internal/webhook/token` and WIF.

Both paths live entirely in `internal/bootstrap` and related admission / token code. Once bootstrapped, the spoke is pure `FleetCluster` machinery — no `Provider` CRD, no runtime dispatch on cloud type.

---

## 12. Target FSM

Per-target rollout state is inline at `PromotionRun.status.targets[i].phase` (`api/v1alpha1/TargetPhase`):

```
Pending
  ↓
Verification          ← optional: cosign signature verification
  ↓
HealthCheck           ← optional: FleetCluster health is fresh + Ready
  ↓
MetricsCheck          ← optional: Prometheus queries pass
  ↓
Soaking               ← optional: minimum time-since-entry has elapsed
  ↓
WaitingApproval       ← optional: Approval CR with name <promotionrun>-<target> exists and is Approved
  ↓
Applying              ← Actuator.Apply() called; FleetCluster.spec.desiredVersion written
  ↓
Converged   |   Failed   |   RolledBack
```

Each optional gate is skipped when its policy is not configured. Phase handlers never silently fall back — an unresolvable gate returns an error and the target stays in its current phase with a failure condition.

### Status shape (bounded)

| Field                              | Bound                                             |
|------------------------------------|---------------------------------------------------|
| `status.targets[]`                 | One row per selected cluster. Current state only. |
| `status.targets[i].gates[]`        | One row per GateTemplate invocation.              |
| `status.report`                    | Compact counter summary + pending approval list.  |
| `status.auditTrail`                | Immutable provenance, capped at 50 entries.       |
| `status.promotionplanProgress[]`        | One row per PromotionPlan node in the DAG.             |
| `status.conditions`                | Standard Kubernetes conditions (`Ready`, etc.).   |

`PromotionRunReportSummary` carries counters and a `pendingApprovals` list only. It does **not** mirror `status.targets[]` or `status.targets[i].gates[]`.

---

## 13. Approval Flow

Approval is intentionally minimal:

1. A target enters `WaitingApproval`.
2. The operator sends a notification (Slack / email / etc.) containing a short-lived signed URL back to `kapro-webhook-server`.
3. A human clicks **approve** or **reject**. The webhook creates an `Approval` CR with the deterministic name **`<promotionrun>-<target>`** in the PromotionRun's namespace, recording `spec.approvedBy` (or `rejected: true`).
4. The `ApprovalGate` (built-in) does a direct `client.Get(<promotionrun>-<target>)` — no label-scan — and returns `Passed` / `Failed` / `Running`.

Identity is deterministic: every `(PromotionRun, target)` pair has at most one `Approval` object. Admission validation enforces that `spec.promotionrun` and `spec.target` match the name.

---

## 14. Notification & Events

- `pkg/notification.Notifier` is an internal contract (not an exposed extension interface yet). Currently ships Slack, email, and generic webhook senders under `internal/notification/engine`.
- The `PromotionRunReconciler` converts `*GatePolicy → pkg/notification.NotificationPolicy` at the call boundary so the notification engine never imports `api/v1alpha1`. This is an internal runtime policy type, not a public CRD.
- Existing inline notifications on gates remain supported and are the active runtime configuration path.
- Every phase transition emits a Kubernetes Event on the `PromotionRun` object.
- Webhook notifications support plain JSON and CloudEvents v1.0 structured JSON. CloudEvents IDs are stable for a given PromotionRun, event type, PromotionPlan, stage, target, and phase so consumers can de-duplicate retries.
- Event type names are the stable integration contract. Phase names are internal FSM detail. See `docs/events.md` for the emitted event catalog and integration examples.

---

## 15. PluginRegistration API Preview

`PluginRegistration` is a status-capable preview for external actuator, gate,
and planner plugins. It is cluster-scoped and records the plugin type, registry name,
protocol, endpoint, timeout, optional namespaced TLS secret reference,
parameters, readiness, version, and capabilities.

The proto contracts live under:

- `spec/kai/v1alpha1/actuator.proto`
- `spec/kgi/v1alpha1/gate.proto`
- `spec/kpi/v1alpha1/planner.proto`

Generated Go stubs are committed beside the proto files. The operator probes
`GetCapabilities` and writes `PluginRegistration.status.ready`, `lastSeen`,
`version`, `contractVersion`, `capabilities`, and conditions. Missing or
unsupported contract versions are reported as `Ready=False` and
`Compatible=False`; the plugin is not loaded for runtime dispatch. When
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`, the operator loads ready registrations with
fresh `status.observedGeneration` into the actuator, gate, and planner runtime
registries. Registration changes are hot-loaded after readiness probes succeed;
stale, incompatible, or deleted registrations are unloaded. Base conformance
harnesses live under `conformance/actuator`, `conformance/gate`, and
`conformance/planner`; plugin authors should run those harnesses against their
implementation. See `docs/plugin-authoring.md` and
`docs/plugin-compatibility.md`.

---

## 16. PromotionTrigger API Preview

`PromotionTrigger` is a safe-by-default API preview for autonomous PromotionRun
creation from verified artifact changes. It is cluster-scoped and supports OCI
source configuration, PromotionRun template configuration, cooldown, max-active
limits, dry-run mode, and status conditions.

The controller observes OCI registries, records the latest matching tag and
digest, and creates digest-pinned `PromotionRun` objects only after safeguards pass.
Created PromotionRuns still use the normal Kapro PromotionPlan; the trigger does not
apply manifests, bypass gates, or promote directly to production.

---

## 17. Non-Goals

Kapro explicitly does **not** aim to:

- Replace Flux or ArgoCD — it orchestrates them.
- Be a generic workflow engine. Stages are rollout waves, not arbitrary DAG nodes.
- Manage in-cluster traffic shaping. Use Argo Rollouts / Flagger for that and gate on their result via `metrics` or `webhook` gates.
- Expose a plugin interface for every internal concern. Actuator, gate, and planner plugins are intentionally narrow and run through explicit KAI, KGI, and KPI contracts.
- Provide a generic cluster-provider abstraction. `FleetCluster` + `internal/bootstrap` is the onboarding path.
- Require AI agents for rollout execution. Future agents may summarize evidence
  and recommend actions under `AgentPolicy`, but Kapro core remains
  deterministic without them.
