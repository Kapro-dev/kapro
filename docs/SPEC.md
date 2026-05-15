# Kapro — Product & Architecture Spec

> **Living document — current state only.** This file describes what is implemented and shipped.
> Planned work lives in `docs/ROADMAP.md`. Nothing is added here until the code is merged and tests pass.
> Always read this before starting any new feature work.

---

## Table of Contents

1. [Vision](#1-vision)
2. [Problem Statement](#2-problem-statement)
3. [Core Mental Model](#3-core-mental-model)
4. [Terminology](#4-terminology)
5. [CRD Inventory](#5-crd-inventory)
6. [Architecture](#6-architecture)
7. [Extension Interfaces](#7-extension-interfaces)
8. [Gate System](#8-gate-system)
9. [Actuator System](#9-actuator-system)
10. [Fleet Onboarding](#10-fleet-onboarding)
11. [Target FSM](#11-target-fsm)
12. [Approval Flow](#12-approval-flow)
13. [Notification & Events](#13-notification--events)
14. [PluginRegistration API Preview](#14-pluginregistration-api-preview)
15. [ReleaseTrigger API Preview](#15-releasetrigger-api-preview)
16. [Non-Goals](#16-non-goals)

---

## 1. Vision

Kapro is a **Kubernetes-native progressive delivery orchestrator for cluster fleets**.

It answers one question deterministically: *"Is it safe to deliver this artifact version to this target cluster right now?"*

Kapro does not replace GitOps tools (Flux, ArgoCD). It **orchestrates** them — sitting above delivery systems as the choreographer that decides *when* and *in what order* clusters receive new versions, based on configurable gate policies: time soaks, Prometheus metrics, human approval, health checks, OCI signature verification, and custom CEL expressions.

**Analogy:** Kubernetes is to containers what Kapro is to delivery waves. Kubernetes manages the lifecycle of containers on nodes. Kapro manages the lifecycle of releases across target clusters in a fleet.

---

## 2. Problem Statement

Platform engineering teams running 10–500 Kubernetes clusters have three bad options today:

- **Manual**: hand-deploy between clusters with no guardrails, no audit trail, no rollback.
- **Bespoke**: per-team promotion pipelines in CI (GitHub Actions, Tekton, Jenkins) with hard-coded targets, no reusable gate logic, brittle failure modes.
- **Partial**: Argo Rollouts handles in-cluster canary but not cross-cluster waves. Flux applies GitOps but not the "should I apply?" decision.

No CNCF-native tool manages the **full pipeline**: artifact → gates → multi-target wave → convergence → rollback — across a fleet, with an auditable per-release history.

---

## 3. Core Mental Model

### Pod analogy

| Kubernetes | Kapro |
|---|---|
| `Node` | `MemberCluster` — a cluster registered in the fleet |
| `Pod` | (the per-target rollout tracked inline in `Release.status.targets[]`) |
| `Container` | Workload running inside a cluster |
| `PodSpec.nodeSelector` | `Stage.clusterSelector` — which clusters a stage targets |
| `Job` | `Release` — owns the full delivery lifecycle, terminates on completion |
| `CronJob` | (future: recurring `Release` trigger) |

### Two-level DAG

```
Release
└── Pipeline DAG (pipeline → pipeline via dependsOn)
    └── Stage DAG (stage → stage via dependsOn inside a pipeline)
        └── clusterSelector → MemberCluster set → per-target rollout tracked in Release.status.targets[]
```

- **Release** owns execution end-to-end and terminates when all pipelines/stages have converged or failed.
- **Pipeline** is a reusable template of stages (no status, no live fields).
- **Stage** selects clusters and carries the gate policy that applies to those clusters.
- **MemberCluster** is pure inventory + observed state; the operator writes `spec.desiredVersion` and reads status.
- **Approval** is a separate CRD that exists only to carry a human "approve / reject" signal for a target awaiting approval.

### What Kapro does **not** have

- No `Sync` CRD. Per-target execution state is inline in `Release.status.targets[]`.
- No `ReleaseReport` / `ReleaseRevision` CRD. Report summary is inline in `Release.status.report`; audit trail is inline in `Release.status.auditTrail`.
- No generic cluster-provider interface. `MemberCluster` is the single cluster CRD; bootstrap lives in code (`internal/bootstrap`).

---

## 4. Terminology

| Term | Meaning |
|------|---------|
| **Artifact** | Declares an OCI repository and a tag resolution policy (semver range, tag list, etc.). Resolves to a concrete `version`. |
| **Pipeline** | Template only. Contains a DAG of `Stage`s. No status. |
| **Stage** | One wave inside a Pipeline. Selects target clusters via `clusterSelector` labels. Carries a `gate` policy. |
| **Release** | The execution owner. References an Artifact and a DAG of Pipelines; drives per-target rollout inline. |
| **MemberCluster** | One workload cluster in the fleet. Holds actuator config, health check config, topology, and observed status (heartbeat + current versions). |
| **Approval** | A CRD carrying a human approve/reject signal for a `(Release, target)` pair. Deterministic name: `<release>-<target>`. |
| **Target** | A `MemberCluster` selected by a stage — i.e. one row in `Release.status.targets[]`. |
| **Gate** | One evaluation step in the target FSM. Returns `Passed`, `Failed`, `Running`, or `Inconclusive`. Stateless. |
| **Actuator** | Driver that applies a version to a target cluster (today: Flux). |

---

## 5. CRD Inventory

| CRD | Kind | Ownership | Scope |
|-----|------|-----------|-------|
| `kapros.kapro.io` | `Kapro` | Platform | Cluster |
| `kaproapps.kapro.io` | `KaproApp` | Platform | Cluster |
| `pipelines.kapro.io` | `Pipeline` | Platform | Cluster |
| `releases.kapro.io` | `Release` | Release engineer / automation | Cluster |
| `releasetriggers.kapro.io` | `ReleaseTrigger` | Platform / automation | Cluster |
| `releasetargets.kapro.io` | `ReleaseTarget` | Controller | Cluster |
| `memberclusters.kapro.io` | `MemberCluster` | Platform | Cluster |
| `pluginregistrations.kapro.io` | `PluginRegistration` | Platform | Cluster |
| `approvals.kapro.io` | `Approval` | Human via webhook | Cluster |
| `agentpolicies.kapro.io` | `AgentPolicy` | Platform | Cluster |

`KaproApp` and `Pipeline` are spec-only template objects. Execution state lives
in `Release`, `ReleaseTarget`, `MemberCluster`, `Approval`, and
`ReleaseTrigger` status.

---

## 6. Architecture

```
┌────────────────────────────── Hub cluster ──────────────────────────────┐
│                                                                         │
│  ┌───────────────────────── kapro-operator ───────────────────────┐     │
│  │                                                                │     │
│  │  ReleaseReconciler     ─── drives the Pipeline DAG, owns the   │     │
│  │                            inline per-target FSM, resolves     │     │
│  │                            gates via gate.Registry             │     │
│  │                                                                │     │
│  │  ApprovalReconciler    ─── watches Approval objects, unblocks  │     │
│  │                            targets in WaitingApproval          │     │
│  │                                                                │     │
│  │  CSRApprovalReconciler ─── approves spoke-cluster CSRs based   │     │
│  │                            on BootstrapToken                   │     │
│  │                                                                │     │
│  │  Admission webhooks    ─── MemberCluster + Approval validators │     │
│  └────────────────────────────────────────────────────────────────┘     │
│                                                                         │
│  ┌────────────────────── kapro-webhook-server ───────────────────┐      │
│  │  POST /approve/:token  → creates Approval(release-target)     │      │
│  │  POST /reject/:token   → creates Approval(release-target)     │      │
│  │  GET  /status/:release → returns Release.status               │      │
│  └───────────────────────────────────────────────────────────────┘      │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
                                   │ outbound
                                   ▼
┌─────────────────────── Spoke cluster (one per member) ──────────────────┐
│                                                                         │
│  kapro-cluster-controller                                               │
│    - polls MemberCluster.spec.desiredVersion on the hub                 │
│    - patches the local delivery system (Flux/Argo) accordingly          │
│    - writes MemberCluster.status (heartbeat + currentVersions)          │
│    - outbound-only HTTPS; authenticates via CSR-issued cert or GCP WIF  │
└─────────────────────────────────────────────────────────────────────────┘
```

Controllers are registered from `pkg/controllermanager/controllers.go`. Hub and spoke share the generated clientset but run different reconciler sets.

---

## 7. Extension Interfaces

Kapro has narrow pluggable interfaces for backend execution, safety evaluation,
and rollout planning. Actuators and gates are named runtime-resolved dispatch
points with conformance suites. The release planner is an in-process framework
for target selection and ordering, modeled after Kubernetes scheduler phases.

| Interface | Go package | Question it answers | Conformance |
|-----------|------------|---------------------|-------------|
| Actuator (KAI) | `pkg/actuator` | "Apply this version to this cluster" | `conformance/actuator` |
| Gate (KGI)     | `pkg/gate`     | "May this target advance?"           | `conformance/gate`     |
| Planner (KPI) | `pkg/planner` | "Which targets should this stage bind, and in what order?" | `conformance/planner` |

Other internal concerns — health checking (`internal/health`), OCI fetch (`internal/oci/oras`), cosign verification (`internal/verification/cosign`), notification (`internal/notification`) — are **not** runtime extension points today. They live as internal packages with fixed implementations.

See `docs/extension-model.md` for the full extension boundary model and the
criteria for adding future plugin or CRD surfaces.

There is **no** cluster-provider interface. Cluster onboarding is concrete, not pluggable (see §10).

---

## 8. Gate System

A `Gate` (`pkg/gate.Gate`) is a stateless evaluator:

```go
type Gate interface {
    Evaluate(ctx context.Context, req Request) (Result, error)
}
```

`Result.Phase` is one of `Passed | Failed | Running | Inconclusive`. All timing, retry, and failure policy live in the `ReleaseReconciler` — the gate just evaluates and returns.

All gate state for a running target is persisted on `Release.status.targets[i].gates[]` in etcd. Controller restarts do not lose gate progress.

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

## 9. Actuator System

An `Actuator` (`pkg/actuator.Actuator`) drives the "apply this version to this cluster" step:

```go
type Actuator interface {
    Apply(ctx context.Context, req ApplyRequest) error
    IsConverged(ctx context.Context, cluster *MemberCluster, version, appKey string) (bool, error)
    Rollback(ctx context.Context, cluster *MemberCluster, previousVersion, appKey string) error
    ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error)
    IsAllConverged(ctx context.Context, cluster *MemberCluster, desiredVersions map[string]string) (bool, error)
}
```

The reference implementation is **Flux** (`internal/actuator/flux`). It writes `MemberCluster.spec.desiredVersion` on the hub; the spoke `kapro-cluster-controller` observes it and reconciles the local Flux `OCIRepository` + `Kustomization`.

Other actuators (Argo, Helm, KServe, Pulumi, raw Kubernetes apply) are future work. The contract is stable.

---

## 10. Fleet Onboarding

Spoke onboarding is **not** a pluggable extension point. It uses two concrete paths:

1. **CSR-based (default).** The spoke cluster-controller submits a Kubernetes `CertificateSigningRequest` with a bootstrap token. The hub's `CSRApprovalReconciler` validates the token and approves the CSR. The spoke then receives a client certificate bound to a `system:kapro:<cluster>` identity.
2. **GCP Workload Identity Federation (optional).** When `MemberCluster.spec.bootstrap.gcp` is set, the spoke exchanges a GCP ID token for hub credentials via `internal/webhook/token` and WIF.

Both paths live entirely in `internal/bootstrap` and related admission / token code. Once bootstrapped, the spoke is pure `MemberCluster` machinery — no `Provider` CRD, no runtime dispatch on cloud type.

---

## 11. Target FSM

Per-target rollout state is inline at `Release.status.targets[i].phase` (`api/v1alpha1/TargetPhase`):

```
Pending
  ↓
Verification          ← optional: cosign signature verification
  ↓
HealthCheck           ← optional: MemberCluster health is fresh + Ready
  ↓
MetricsCheck          ← optional: Prometheus queries pass
  ↓
Soaking               ← optional: minimum time-since-entry has elapsed
  ↓
WaitingApproval       ← optional: Approval CR with name <release>-<target> exists and is Approved
  ↓
Applying              ← Actuator.Apply() called; MemberCluster.spec.desiredVersion written
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
| `status.pipelineProgress[]`        | One row per pipeline node in the DAG.             |
| `status.conditions`                | Standard Kubernetes conditions (`Ready`, etc.).   |

`ReleaseReportSummary` carries counters and a `pendingApprovals` list only. It does **not** mirror `status.targets[]` or `status.targets[i].gates[]`.

---

## 12. Approval Flow

Approval is intentionally minimal:

1. A target enters `WaitingApproval`.
2. The operator sends a notification (Slack / email / etc.) containing a short-lived signed URL back to `kapro-webhook-server`.
3. A human clicks **approve** or **reject**. The webhook creates an `Approval` CR with the deterministic name **`<release>-<target>`** in the release's namespace, recording `spec.approvedBy` (or `rejected: true`).
4. The `ApprovalGate` (built-in) does a direct `client.Get(<release>-<target>)` — no label-scan — and returns `Passed` / `Failed` / `Running`.

Identity is deterministic: every `(Release, target)` pair has at most one `Approval` object. Admission validation enforces that `spec.release` and `spec.target` match the name.

---

## 13. Notification & Events

- `pkg/notification.Notifier` is an internal contract (not an exposed extension interface yet). Currently ships Slack, email, and generic webhook senders under `internal/notification/engine`.
- The `ReleaseReconciler` converts `*GatePolicy → NotificationPolicy` at the call boundary so the notification engine never imports `api/v1alpha1`.
- Every phase transition emits a Kubernetes Event on the `Release` object.
- Webhook notifications support plain JSON and CloudEvents v1.0 structured JSON. CloudEvents IDs are stable for a given release, event type, pipeline, stage, target, and phase so consumers can de-duplicate retries.
- Event type names are the stable integration contract. Phase names are internal FSM detail. See `docs/events.md` for the emitted event catalog and integration examples.

---

## 14. PluginRegistration API Preview

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
`version`, `capabilities`, and conditions. When
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`, the operator loads ready registrations with
fresh `status.observedGeneration` into the actuator and gate registries once at
startup. Planner plugin registration is probed and reported in status; runtime
dispatch is future work. Dynamic hot reload is future work. Base conformance
harnesses live under `conformance/actuator`, `conformance/gate`, and
`conformance/planner`; plugin authors should run those harnesses against their
implementation. See `docs/plugin-authoring.md`.

---

## 15. ReleaseTrigger API Preview

`ReleaseTrigger` is a safe-by-default API preview for autonomous Release
creation from verified artifact changes. It is cluster-scoped and supports OCI
source configuration, release template configuration, cooldown, max-active
limits, dry-run mode, and status conditions.

The controller observes OCI registries, records the latest matching tag and
digest, and creates digest-pinned `Release` objects only after safeguards pass.
Created releases still use the normal Kapro pipeline; the trigger does not
apply manifests, bypass gates, or promote directly to production.

---

## 16. Non-Goals

Kapro explicitly does **not** aim to:

- Replace Flux or ArgoCD — it orchestrates them.
- Be a generic workflow engine. Stages are rollout waves, not arbitrary DAG nodes.
- Manage in-cluster traffic shaping. Use Argo Rollouts / Flagger for that and gate on their result via `metrics` or `webhook` gates.
- Expose a plugin interface for every internal concern. Actuator and gate plugins have startup-time runtime dispatch; planner plugins are API-preview and status-probed only.
- Provide a generic cluster-provider abstraction. `MemberCluster` + `internal/bootstrap` is the onboarding path.
