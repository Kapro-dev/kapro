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
7. [Gate System](#7-gate-system)
8. [Actuator System](#8-actuator-system)
9. [Fleet Management](#9-fleet-management)
10. [Sync FSM](#10-sync-fsm)
11. [Notification & Events](#11-notification--events)
12. [MVP Scope](#12-mvp-scope)
13. [User Stories](#13-user-stories)
14. [Requirements](#14-requirements)
15. [Success Metrics](#15-success-metrics)
16. [Non-Goals](#16-non-goals)
17. [Open Questions](#17-open-questions)

---

## 1. Vision

Kapro is a **Kubernetes-native progressive delivery orchestrator for cluster fleets**.

It answers one question deterministically: *"Is it safe to deliver this artifact version to this target right now?"*

Kapro does not replace GitOps tools (Flux, ArgoCD). It orchestrates them — sitting above delivery systems as the choreographer that decides *when* and *in what order* environments receive new versions, based on configurable gate policies: time soaks, Prometheus metrics, human approval, health checks, OCI signature verification, and custom CEL expressions.

**Analogy:** Kubernetes is to containers what Kapro is to delivery waves. Kubernetes manages the lifecycle of containers on nodes. Kapro manages the lifecycle of releases across environments on cluster fleets.

---

## 2. Problem Statement

### Who is affected

Platform engineering teams operating 10–500 Kubernetes clusters across environments (dev, staging, canary, regional prod). Today, progressive delivery across a fleet is either:

- **Manual**: engineers hand-deploy between environments with no guardrails, no audit trail, and no automatic rollback.
- **Bespoke**: each team scripts their own promotion pipeline in CI (GitHub Actions, Tekton, Jenkins) with hard-coded target sequences, no reusable gate logic, and brittle failure modes.
- **Partial**: tools like Argo Rollouts handle in-cluster canary but not cross-cluster waves. Flux handles GitOps apply but not the "should I apply?" decision.

### The gap

No CNCF-native tool manages the **full pipeline**: artifact → gates → multi-target wave → convergence verification → rollback — across a fleet of heterogeneous clusters, with an auditable history per release.

### Cost of not solving it

- Production incidents from under-gated promotions (missing soak period, no metric check).
- Engineering time wasted hand-managing target sequences.
- No audit trail for compliance (who approved, what gate passed, when each cluster got which version).
- Rollback is a fire drill: no first-class rollback primitive, no record of prior states.

---

## 3. Core Mental Model

### The Pod analogy

| Kubernetes | Kapro |
|---|---|
| `Node` | `ManagedCluster` — a cluster registered in the fleet |
| `Pod` | `Target` — lifecycle owner; selects clusters like Pod selects nodes |
| `Container` | Workload running inside a cluster |
| `init container` | `Sync` in gate phases (Verification → HealthCheck → Soaking → ...) |
| `PodSpec.nodeSelector` | `Stage.clusterSelector` — selects which clusters a stage targets |
| `PodStatus.containerStatuses[]` | `Sync.Status.Gates[]` — authoritative gate run history |
| `Job` | `Release` — owns the full delivery lifecycle, terminates on completion |
| `CRI` | `gate.Gate` interface — Kapro doesn't care which gate evaluates; it calls the interface |

### The two-level DAG

```
Release
  └── Pipeline A (dependsOn: [])
        └── Stage: canary    → selects 1 cluster  → Sync per cluster
        └── Stage: regional  → selects 5 clusters → Sync per cluster (parallel)
        └── Stage: prod      → selects all        → Sync per cluster (parallel)
  └── Pipeline B (dependsOn: [Pipeline A])
        └── Stage: ml-prod   → selects GPU clusters
```

- **Pipeline DAG**: `Release.spec.pipelines[].dependsOn` — pipelines run in dependency order.
- **Stage DAG**: `Pipeline.spec.stages[].dependsOn` — stages within a pipeline run in dependency order.
- **Sync**: one per `(Release, Pipeline, Stage, Target)` tuple. All Syncs within a stage fan-out in parallel.

### Immutable delivery

Releases are immutable records of intent. A `Release` declares "deliver artifact digest `sha256:abc` through pipeline `standard-rollout`." It never changes. Rollback is a new `Release` pointing at an older OCI digest. This makes the audit trail append-only.

---

## 4. Terminology

| Term | Definition |
|---|---|
| **Artifact** | An OCI bundle (image, Helm chart, kustomize overlay) at a specific digest. Immutable once created. |
| **Target** | A named delivery target owning one cluster's lifecycle. Analogous to a Pod — it manages the cluster like a Pod manages containers. |
| **ManagedCluster** | Fleet registry entry for a registered cluster. Written by `kapro-cluster-controller` running on the spoke cluster. Analogous to a Node object. |
| **Pipeline** | A DAG of Stages. Defines the delivery sequence. Reusable across Releases. |
| **Stage** | One wave inside a Pipeline. Selects target clusters via `clusterSelector` labels. Creates one Sync per matched Target. |
| **Release** | The user-facing delivery trigger. Owns the full two-level DAG. Immutable once created. Terminal states: `Complete` or `Failed`. |
| **Sync** | System-managed object for one `(Release, Pipeline, Stage, Target)`. Drives the gate FSM from `Pending` → `Converged` or `Failed`. |
| **GatePolicy** | Reusable rules applied to a Stage: soak time, metric thresholds, approval config, notification channels. |
| **GateTemplate** | Reusable parameterised gate evaluation unit. Types: `cel`, `job`, `webhook`. |
| **Approval** | Human (or automation) signal to unblock a `Sync` in `WaitingApproval`. |
| **ReleaseReport** | System-generated audit record aggregating all Syncs for a Release. |
| **BootstrapToken** | Short-lived HMAC token for `kapro-cluster-controller` first registration. |
| **Gate** | One evaluation step in the Sync FSM. Returns `Passed`, `Failed`, or `Inconclusive`. Stateless. |
| **Actuator** | The delivery backend. Receives an `ApplyRequest` and makes the cluster converge. MVP: Flux only. |
| **Provider** | Resolves how Kapro connects to a workload cluster. MVP: CRD provider (kapro-cluster-controller pattern). |

---

## 5. CRD Inventory

### User-facing objects `[MVP]`

| CRD | Kind | Description |
|---|---|---|
| `artifacts.kapro.io` | `Artifact` | Immutable OCI bundle reference |
| `memberclusters.kapro.io` | `Target` | Delivery target; owns cluster lifecycle |
| `pipelines.kapro.io` | `Pipeline` | DAG of Stages defining delivery sequence |
| `releases.kapro.io` | `Release` | Delivery trigger; owns the two-level DAG |
| `gatepolicies.kapro.io` | `GatePolicy` | Reusable gate rules (soak, metrics, approval, notifications) |
| `gatetemplates.kapro.io` | `GateTemplate` | Parameterised gate evaluation units (CEL, Job, Webhook) |
| `managedclusters.kapro.io` | `ManagedCluster` | Fleet registry entry; written by cluster controller |

### System / internal objects `[MVP]`

| CRD | Kind | Description |
|---|---|---|
| `syncs.kapro.io` | `Sync` | One gate→apply→converge cycle per (Release, Pipeline, Stage, Env) |
| `approvals.kapro.io` | `Approval` | Human gate signal |
| `releasereports.kapro.io` | `ReleaseReport` | Audit trail per Release |
| `bootstraptokens.kapro.io` | `BootstrapToken` | Short-lived cluster registration token |

### Removed in MVP (cut)

The following types were considered and **deliberately removed** from the MVP:

| Removed CRD | Reason |
|---|---|
| `ReleaseTrigger` | Autonomous triggers (MLflow, OCI, Prometheus watches) — post-MVP |
| `PluginGateway` | gRPC plugin bridge infrastructure — post-MVP |
| `PluginRegistration` | Plugin self-registration CRD — post-MVP |

---

## 6. Architecture

### Component map

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Control Plane                                  │
│                                                                       │
│  ┌───────────────────────────────────────────────────────────────┐   │
│  │                    kapro-operator                              │   │
│  │                                                               │   │
│  │  ReleaseReconciler   ──── owns two-level DAG, creates Syncs  │   │
│  │  SyncReconciler      ──── drives gate FSM, calls actuator    │   │
│  │  PipelineReconciler  ──── aggregates stage progress          │   │
│  │  ApprovalReconciler  ──── unblocks WaitingApproval Syncs     │   │
│  │  ReleaseReportReconciler ─ aggregates audit records          │   │
│  │  BootstrapTokenReconciler ─ issues spoke registration tokens │   │
│  │                                                               │   │
│  │  Admission Webhooks:                                          │   │
│  │    /mutate-approval    (injects approvedBy from k8s identity) │   │
│  │    /validate-release   (artifact + pipelines required)        │   │
│  │    /validate-target (actuator type must be flux)         │   │
│  │    /validate-pipeline  (stage names unique, no cycles)        │   │
│  │                                                               │   │
│  │  Approval Webhook Server  :8091                               │   │
│  │    POST /approve/:token   → creates Approval CR               │   │
│  │    POST /reject/:token    → annotates Sync for failure        │   │
│  │    GET  /status/:sync     → returns current Sync phase        │   │
│  └───────────────────────────────────────────────────────────────┘   │
│                                                                       │
│  ┌─────────────────────────┐   ┌─────────────────────────────────┐   │
│  │   kapro-cluster-controller  │   │   kapro CLI (kubectl plugin)    │   │
│  │   (runs per spoke cluster)  │   │   kapro get releases            │   │
│  │   POSTs heartbeat to hub    │   │   kapro approve <sync>          │   │
│  │   via HTTP /heartbeat :9090 │   │   kapro rollback <release>      │   │
│  └─────────────────────────┘   └─────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
                          │ Flux HelmRelease/OCIRepository patch
                          ▼
              ┌────────────────────────┐
              │   Spoke Cluster         │
              │   Flux (source of truth)│
              └────────────────────────┘
```

### KXI — The Kapro Extension Interface Family

All Kapro extension points follow the KXI design philosophy, modelled on Kubernetes CRI/CNI/CSI. See **ADR-007** for the full specification.

**Six design axioms** every KXI interface obeys:
1. **One interface, one question** — no two-in-one interfaces
2. **Stateless implementations** — runtime state lives in etcd
3. **Concurrent-safe** — documented and required in godoc
4. **Nop implementations** — every interface ships a `Nop*` for testing
5. **Compile-time checks** — `var _ Interface = (*ConcreteType)(nil)` everywhere
6. **Conformance suite** — `conformance/X/RunSuite(t, impl)` for every interface

**The KXI family:**

```
KXI   │ Interface                   │ Question answered                │ Pkg
──────┼─────────────────────────────┼──────────────────────────────────┼──────────────────
KGI   │ gate.Gate                   │ Is it safe to advance this Sync? │ pkg/gate
KAI   │ actuator.Actuator           │ Apply / converge / rollback      │ pkg/actuator
KCI-C │ provider.Connector          │ Direct-connect to cluster        │ pkg/provider
KCI-R │ provider.RegistrationReader │ Read cluster state from CRDs     │ pkg/provider
KNI   │ notification.Notifier       │ Fan out lifecycle events         │ pkg/notification
KHI   │ health.Assessor             │ Are workloads healthy?           │ pkg/health
KRI   │ oci.Service                 │ Inspect/promote OCI artifacts    │ pkg/oci
KVI   │ verification.Verifier       │ Is this artifact signature valid?│ pkg/verification
KSI   │ scheduler.Plugin (v0.2)     │ Which stage runs next?           │ pkg/scheduler
```

**Kubernetes analogy:**

```
KGI → Admission Webhook   │  KAI → CRI (Container Runtime Interface)
| `KCI` → CCM (Cloud Manager) │  `KNI` → Audit Event Sink
KHI → Readiness Probe     │  KRI → Image Pull   │  KVI → Image Verify
KSI → Scheduler Plugins   │
```

### Generic Registry

All KXI registries use `pkg/registry.Registry[T]` — a single generic, thread-safe dispatch table. Per-type registries embed it:

```go
// pkg/registry/registry.go
type Registry[T any] struct { ... }
func New[T any](name string) *Registry[T]
func (r *Registry[T]) Register(typeName string, impl T) error
func (r *Registry[T]) MustRegister(typeName string, impl T)
func (r *Registry[T]) Resolve(typeName string) (T, error)
func (r *Registry[T]) Has(typeName string) bool
func (r *Registry[T]) Names() []string

// pkg/actuator/registry.go — KAI dispatch (type: flux | argocd | ...)
type Registry struct { *pkgregistry.Registry[Actuator] }

// pkg/provider/registry.go — KCI Connector dispatch (type: gke | eks | aks | ...)
type Registry struct { *pkgregistry.Registry[Connector] }

// pkg/gate/registry.go — KGI template-dispatch (type: cel | job | webhook | external...)
type Registry struct { *pkgregistry.Registry[Gate] }
```

The gate registry is the open extension point for `GateTemplate.spec.type`. Built-in types (`cel`, `job`, `webhook`) are registered in `BuildGateRegistry`. External gate types register at startup without modifying the core controller:

```go
// main.go — wire in an external gate type
cc.GateRegistry.MustRegister("argo-analysis", &mygate.ArgoAnalysisGate{...})
cc.GateRegistry.MustRegister("datadog-monitor", &mygate.DatadogGate{...})
```

### KNI decoupling

`pkg/notification.Notifier` has zero dependency on `api/v1alpha1`. The `SyncReconciler` converts `*GatePolicy → NotificationPolicy` at the call boundary via `notificationPolicyFrom()`. External Notifier implementations never need to import Kapro's CRD package:

```go
// pkg/notification — pure value types, no api/v1alpha1 import
type NotificationPolicy struct { Channels []Channel }
type Channel struct { Type, Target string; Email *EmailConfig }

// Notifier interface — clean, no CRD types
type Notifier interface {
    Notify(ctx context.Context, event Event, policy NotificationPolicy)
}
```

### Interface layer (`pkg/`)

All core abstractions live in `pkg/` as pure Go interfaces. `internal/` holds implementations. This boundary is never crossed the wrong way — `internal/` depends on `pkg/`, never the reverse.

| KXI | Package | MVP Implementation | Nop |
|-----|---------|-------------------|-----|
| KGI `gate.Gate` | `pkg/gate` | SoakGate, MetricsGate, ApprovalGate, VerificationGate, CELGate, JobGate, WebhookGate | — |
| KAI `actuator.Actuator` | `pkg/actuator` | FluxActuator | — |
| KCI `provider.Connector` | `pkg/provider` | — (all Path B connectors in ROADMAP.md) | NopConnector ✅ |
| KCI `provider.RegistrationReader` | `pkg/provider` | CRDProvider | — |
| KNI `notification.Notifier` | `pkg/notification` | Dispatcher, EngineNotifier | NopNotifier ✅ |
| KHI `health.Assessor` | `pkg/health` | GitopsHealthAssessor | NopAssessor ✅ |
| KRI `oci.Service` | `pkg/oci` | ORASService | NopOCIService ✅ |
| KVI `verification.Verifier` | `pkg/verification` | CosignVerifier | NopVerifier ✅ |

### Controller manager pattern

Modelled after `k8s.io/cloud-provider` CCM. Every controller is registered as an `InitFunc`:

```go
Register("release",       startReleaseController)
Register("sync",          startSyncController)
Register("releasereport", startReleaseReportController)
Register("pipeline",      startPipelineController)
Register("approval",      startApprovalController)
Register("bootstraptoken",startBootstrapTokenController)
```

Selective deployment via `KAPRO_CONTROLLERS=*,-releasereport`. Adding a controller = new `InitFunc` + one `Register()` call.

The `ControllerContext` struct is the dependency bundle injected into every controller:

```go
type ControllerContext struct {
    Manager          ctrl.Manager
    Recorder         record.EventRecorder
    ActuatorRegistry *actuator.Registry    // KAI: flux → FluxActuator
    ProviderRegistry *provider.Registry    // KCI: dispatches by spec.provider.type
    Gates            GateSet               // KGI: 4 built-in gates
    HealthAssessor   health.Assessor       // KHI
    Notifier         notification.Notifier // KNI
    OCIService       oci.Service           // KRI
    ApprovalSecret   []byte
    ExternalURL      string
}
```

### Gate wiring (MVP)

`BuildGateSet(client.Client)` is the single complete construction point for all four built-in gates:

```go
GateSet{
    Soak:         &SoakGate{},
    Metrics:      &MetricsGate{},
    Approval:     &ApprovalGate{Client: c},
    Verification: &VerificationGate{Verifier: cosign, KeyReader: secretReader},
}
```

`CELGate`, `JobGate`, and `WebhookGate` are constructed per-call inside `gateForTemplate()` — they carry no state and do not need to be in the GateSet.

### Adding a new extension

```
Goal                                  │ Implement             │ Register in
──────────────────────────────────────┼───────────────────────┼─────────────────────
New gate type (e.g. OPA)              │ gate.Gate             │ gateForTemplate()
New delivery backend (e.g. ArgoCD)    │ actuator.Actuator     │ actuator.Registry
New cloud provider direct-connect     │ provider.Connector    │ provider.Registry
New cloud provider outbound           │ Deploy cluster-ctrl   │ BootstrapToken flow
New notification channel              │ notification.Notifier │ ControllerContext
New health assessor                   │ health.Assessor       │ ControllerContext
New OCI backend (e.g. Harbor native)  │ oci.Service           │ ControllerContext
New signature verifier (e.g. Notary)  │ verification.Verifier │ VerificationGate
New scheduling policy (v0.2)          │ scheduler.FilterPlugin│ SchedulerRegistry
```

---

## 7. Gate System

### How gates work

A `Gate` is a stateless evaluator that answers: *"can this Sync advance right now?"*

```go
type Gate interface {
    Evaluate(ctx context.Context, req Request) (Result, error)
}
```

`Result.Phase` is one of `Passed | Failed | Running | Inconclusive`. The SyncReconciler owns all timing, retry, and failure policy logic — the gate just evaluates and returns.

All gate state is stored on `Sync.Status.Gates[]` in etcd. Restarting the controller does not lose gate progress.

### MVP gates `[MVP]`

| Gate | Type | Trigger | Pass condition |
|---|---|---|---|
| **SoakGate** | Built-in | `GatePolicy.spec.gate.soakTime` | `now - Sync.Status.StartedAt >= soakTime` |
| **MetricsGate** | Built-in | `GatePolicy.spec.gate.metrics[]` | All Prometheus queries return non-empty vector |
| **ApprovalGate** | Built-in | `GatePolicy.spec.approval.required: true` | Matching `Approval` CR exists in namespace |
| **VerificationGate** | Built-in | `GatePolicy.spec.gate.verification` | cosign verifies OCI artifact signature |
| **CELGate** | GateTemplate `type: cel` | `GateTemplate.spec.cel.expression` | CEL expression evaluates to `true` |
| **JobGate** | GateTemplate `type: job` | `GateTemplate.spec.job` | Kubernetes Job exits 0 |
| **WebhookGate** | GateTemplate `type: webhook` | `GateTemplate.spec.webhook.url` | HTTP POST returns `{"phase":"Passed"}` |

### CEL expression context

Available variables in CEL expressions:

```
args.my_param          // GateTemplate args, policy overrides, sync context
target.name       // Target name
target.labels.*   // target labels (GPU type, region, tier)
sync.name              // Sync name
sync.version           // Artifact version string
sync.target    // Target name
sync.releaseRef        // Release name
```

Example: `sync.version.startsWith("v") && target.labels["tier"] != "prod"`

### GateTemplate parameterisation

```yaml
apiVersion: kapro.io/v1alpha1
kind: GateTemplate
metadata:
  name: canary-error-rate
spec:
  type: cel
  args:
    - name: threshold
      value: "0.01"   # default; overridable per policy
  cel:
    expression: |
      args.error_rate < double(args.threshold)
```

### Conformance

Every gate implementation must pass `conformance/gate.RunSuite(t, gate)`. The suite verifies: nil-safe on nil Sync, nil-safe on nil Policy, returns Passed when condition met, returns non-empty RetryAfter when blocking.

---

## 8. Actuator System

### What an actuator does

An `Actuator` applies a version to an target and reports convergence:

```go
type Actuator interface {
    Apply(ctx context.Context, req ApplyRequest) error
    // IsConverged polls whether the target cluster has applied version under appKey.
    // appKey is the key in ManagedCluster.status.currentVersions (e.g. "default").
    // v0.2: appKey added explicitly; previously resolved internally from ManagedCluster.spec.
    IsConverged(ctx context.Context, env *Target, version, appKey string) (bool, error)
    Rollback(ctx context.Context, env *Target, previousVersion string) error
}
```

The SyncReconciler calls `Apply()` when entering `Applying` phase, then polls `IsConverged()` until the cluster reports the new version is healthy. `Rollback()` is called when a gate fails after apply (in-flight rollback).

### MVP actuator `[MVP]`

**FluxActuator** — patches the `OCIRepository` tag on the target cluster's Flux installation and waits for the `Kustomization` to report `Ready=True`.

```yaml
spec:
  actuator:
    type: flux
    flux:
      namespace: flux-system
      ociRepository: my-app
      kustomizationPath: ./deploy
```

All actuators register at startup via `actuator.Registry.Register("name", impl)` — no types.go change required. Additional actuator implementations are tracked in `docs/ROADMAP.md`.

---

## 9. Fleet Management

### Architecture: NameNode / DataNode heartbeat pattern

Kapro's fleet connectivity model is directly analogous to the Hadoop NameNode/DataNode architecture:

| Hadoop | Kapro |
|--------|-------|
| NameNode | `kapro-operator` (hub) — owns fleet state, issues desired versions |
| DataNode | `kapro-cluster-controller` (spoke) — reports local state, applies desired versions |
| DataNode heartbeat to NameNode | `POST /heartbeat` from spoke to hub every 30s |
| Block report | `HeartbeatRequest` payload (phase, currentVersions, health, deliverySystem) |
| NameNode response | `HeartbeatResponse` (DesiredVersion, DesiredAppKey, OCIRepository name) |

**Core invariant:** The hub never dials into a spoke. All communication is spoke-initiated outbound HTTPS. This works in any network topology — air-gap, multi-cloud, on-prem, private GKE clusters, cross-project, hybrid cloud.

### Heartbeat protocol

```
Spoke cluster-controller                         Hub kapro-operator
         │                                               │
         │  POST /heartbeat  (port 9090)                 │
         │  Authorization: Bearer <sa-token>             │
         │  {                                            │
         │    "clusterName": "eu-prod-1",                │
         │    "phase": "Converged",                      │
         │    "currentVersions": {"default": "v1.2.3"},  │
         │    "health": {"healthy": true, ...},          │
         │    "deliverySystem": "flux"                   │
         │  }                                            │
         │ ──────────────────────────────────────────►  │
         │                                               │  1. TokenReview → validate SA token
         │                                               │  2. Extract cluster name from SA username:
         │                                               │     kapro-cluster-<clusterName>
         │                                               │  3. Patch ManagedCluster.status
         │                                               │  4. Read ManagedCluster.spec + Target
         │  {                                            │
         │    "desiredVersion": "v1.3.0",                │
         │    "desiredAppKey": "default",                │
         │    "ociRepository": "my-app"                  │
         │  }                                            │
         │ ◄──────────────────────────────────────────  │
         │                                               │
         │  Apply desired version via Flux               │
         │  (patch local OCIRepository tag)              │
```

**Authentication flow:**
- Registration: HMAC `BootstrapToken` (1-hour TTL, single-use) → hub creates ServiceAccount `kapro-cluster-<name>`
- Ongoing: hub issues long-lived SA token → spoke stores locally → used as Bearer token for all `/heartbeat` calls
- Hub validates every heartbeat via K8s TokenReview API — spoke SA needs no K8s RBAC on hub after registration
- SA username encodes cluster identity: `system:serviceaccount:kapro-system:kapro-cluster-<clusterName>`

### Two cluster onboarding paths

Kapro supports two connectivity models. **Path A is the canonical choice for all deployments** — it works universally. Path B is an optional enhancement for public cloud clusters only.

```
Path A: Outbound Heartbeat / CRD Provider   ← CANONICAL (all clouds, all topologies)
──────────────────────────────────────────────────────────────────────────────────────
Spoke cluster runs kapro-cluster-controller (Helm, one per cluster).
Controller POSTs HeartbeatRequest to hub /heartbeat endpoint every 30s.
Hub validates Bearer token, patches ManagedCluster.status, responds with desired state.
Spoke applies desired version to local Flux installation.

Works for:  GKE (any project, private or public), EKS, AKS, on-prem, air-gap,
            cross-project, cross-account, hybrid cloud, no VPN required.
Requires:   Outbound HTTPS from spoke to ONE endpoint (hub LoadBalancer port 9090).
Bootstrap:  BootstrapToken HMAC (1-hour TTL, single use).
MemberCluster spec: spec.provider.type: "" (or "crd")

Path B: Direct Connect / KCI Connector      ← PUBLIC CLOUD ONLY (no-agent option)
──────────────────────────────────────────────────────────────────────────────────────
Hub authenticates to cloud API using cloud IAM (Workload Identity / IRSA / Managed Identity).
Hub fetches cluster credentials on demand — no cluster-controller agent needed on spoke.
Requires:   Hub-to-spoke network access + cloud IAM binding (not available for private clusters).
MemberCluster spec: spec.provider.type: gke | aks | digitalocean | stackit

⚠️  Path B CANNOT be used for:
    - Private GKE clusters (no public endpoint on API server)
    - Cross-project GKE without authorized networks or VPC peering
    - On-prem / air-gapped clusters
    - Multi-account AWS without cross-account IAM trust
    Use Path A for all of these.
```

**Default is Path A.** `spec.provider.type: ""` resolves to CRD provider (backward compatible).

### Supported cloud providers

Path A (CRD provider) is fully implemented for all clouds. Path B (direct-connect) implementation status is tracked in `docs/ROADMAP.md`.

| Cloud | Path A | Path B | Auth (Path B) |
|-------|--------|--------|----------------|
| GCP / GKE | ✅ | ROADMAP.md | Workload Identity (keyless) |
| AWS / EKS | ✅ | ROADMAP.md | IRSA + STS (keyless) |
| Azure / AKS | ✅ | ROADMAP.md | Managed Identity + AAD OIDC (keyless) |
| DigitalOcean | ✅ | ROADMAP.md | API token in Secret |
| StackIT | ✅ | ROADMAP.md | Service Account key in Secret |
| On-prem / air-gap | ✅ | N/A | — |

### kapro-cluster-controller `[MVP]` (Path A)

A lightweight controller deployed **on each spoke cluster** (via Helm). It:

1. Reads its own cluster identity from a `ConfigMap` (`kapro-system/cluster-identity`).
2. Every 30s: reads local Flux state (phase, currentVersions, health), then **POSTs a `HeartbeatRequest` to the hub's `/heartbeat` endpoint** (port 9090) using a long-lived SA Bearer token.
3. Hub responds with `HeartbeatResponse` containing `DesiredVersion`, `DesiredAppKey`, and `OCIRepository` name.
4. Controller applies the desired version by patching the local Flux `OCIRepository` tag.
5. Reports: Kubernetes version, Flux version, node count, region, **cloud, zone, accountID**, cluster health, active release versions.

The hub's registration server (`internal/registration/server.go`) handles heartbeats:
- Validates the Bearer token via K8s TokenReview API
- Extracts cluster identity from SA username (`kapro-cluster-<clusterName>`)
- Patches `ManagedCluster.status` with received health/version data
- Returns desired state from `ManagedCluster.spec` + `Target.spec.actuator.flux`

**Key property:** The hub writes `ManagedCluster.status` itself — the spoke never writes directly to the hub's K8s API. The spoke only needs outbound HTTPS to one endpoint.

### target ↔ ManagedCluster binding

```yaml
# target selects clusters by label
kind: MemberCluster
spec:
  clusterSelector:
    matchLabels:
      region: us-east-1
      tier: canary
```

The `ReleaseReconciler` resolves `Target → ManagedCluster` at sync creation time. If no ManagedCluster matches the selector, the Sync waits in `Pending` with a `NoClusterFound` condition.

### Cluster topology metadata `[MVP]`

`ManagedCluster.spec.capabilities` carries topology for cloud-aware and GPU-aware stage routing:

```yaml
spec:
  capabilities:
    k8sVersion: "1.30"
    fluxVersion: "2.3"
    nodeCount: 12
    region: europe-west1
    cloud: gcp              # gcp | aws | azure | digitalocean | stackit | on-prem
    zone: europe-west1-b
    accountID: my-gcp-project
    clusterID: my-gke-cluster
```

Pipeline stages use this for cloud-aware and accelerator-aware delivery waves (e.g. "promote to EU clusters before US clusters", "promote to H100 before A100").

### target provider config

Path A is the default and requires no provider fields. `spec.provider.type: ""` resolves to CRD provider (backward compatible).

```yaml
# Path A (default) — no provider fields needed
kind: MemberCluster
spec:
  provider: {}   # resolves to CRD provider
```

Path B `ProviderSpec` fields (`gke`, `aks`, `digitalOcean`, `stackit`) are defined in `api/v1alpha1/types.go` and the CRD schema is ready. Connector implementations are tracked in `docs/ROADMAP.md`.

**Security invariant:** Credentials (API tokens, SA keys) are **never stored in CRD fields**. Always referenced by Secret name in `kapro-system`. Workload Identity / IRSA (keyless) is preferred where available.

### Bootstrap flow (Path A — all clouds)

```
1. Platform engineer creates BootstrapToken CR on hub:
   kubectl apply -f - <<EOF
   apiVersion: kapro.io/v1alpha1
   kind: BootstrapToken
   metadata:
     name: gke-prod-eu
     namespace: kapro-system
   spec:
     target: gke-prod-eu
     ttl: 1h
   EOF

2. Kapro operator generates HMAC token → stores in Secret bootstrap-token-gke-prod-eu

3. Engineer deploys cluster-controller on spoke (any cloud, any topology):
   helm install kapro-cc kapro/cluster-controller \
     --set hub.url=https://kapro.internal \
     --set hub.bootstrapToken=<token-from-secret> \
     --set cluster.target=gke-prod-eu \
     --set cluster.cloud=gcp \
     --set cluster.region=europe-west1

4. cluster-controller POSTs to hub /register with HMAC token

5. Hub validates HMAC, creates ServiceAccount kapro-cluster-gke-prod-eu in kapro-system,
   issues long-lived SA token, responds with token + CA bundle

6. cluster-controller stores token locally (in-memory + Secret kapro-system/hub-credential)
   Token refresh loop keeps it alive; no K8s RBAC on hub required after this point

7. BootstrapToken is single-use — deleted or expires after first successful registration

8. cluster-controller begins 30s heartbeat loop:
   POST /heartbeat → hub patches ManagedCluster.status → controller applies desired version

9. target is live. Create your first Release.
```

### Provider dispatch (runtime)

`EnvironmentSpec.provider.type` is resolved at reconcile time via `provider.Registry` (mirrors `actuator.Registry`):

```
type = ""    → CRDProvider.GetRegistration()  (Path A — fully implemented)
type = "gke" → GKEConnector.Connect()         (Path B — ROADMAP.md v0.3)
type = "aks" → AKSConnector.Connect()         (Path B — ROADMAP.md v0.4)
...
```

New providers are registered at startup in `cmd/operator/main.go` — no changes to the FSM or gate system.

---

## 10. Sync FSM

The Sync state machine is the core of Kapro. Every state transition is persisted to etcd. The controller is fully re-entrant — it can restart at any state without losing progress.

```
                    ┌──────────┐
                    │ Pending  │  ← created by ReleaseReconciler
                    └────┬─────┘
                         │ cluster found + policy resolved
                    ┌────▼────────┐
                    │ Verification│  ← cosign signature check
                    └────┬────────┘
                         │ passed (or verification disabled)
                    ┌────▼────────┐
                    │ HealthCheck │  ← k8s workload health
                    └────┬────────┘
                         │ passed (or health disabled)
                    ┌────▼────────┐
                    │   Soaking   │  ← time-based bake period
                    └────┬────────┘
                         │ soak elapsed
                    ┌────▼────────┐
                    │MetricsCheck │  ← Prometheus queries + GateTemplates
                    └────┬────────┘
                         │ all gates passed
                    ┌────▼──────────────┐
                    │ WaitingApproval   │  ← human gate (if required)
                    └────┬──────────────┘
                         │ Approval CR created
                    ┌────▼────┐
                    │ Applying │  ← actuator.Apply() called
                    └────┬─────┘
                         │ convergence verified
          ┌──────────────┴─────────────────┐
     ┌────▼────┐                      ┌────▼────┐
     │Converged│                      │  Failed │
     └─────────┘                      └─────────┘
```

### Phase semantics

| Phase | Entry condition | Exit condition |
|---|---|---|
| `Pending` | Created by ReleaseReconciler | ManagedCluster found + GatePolicy resolved |
| `Verification` | Entered from Pending | Artifact signature verified (or `verification.skip: true`) |
| `HealthCheck` | Entered from Verification | All workloads `Healthy` (or `healthCheck` not configured) |
| `Soaking` | Entered from HealthCheck | `now - StartedAt >= soakTime` (or no soakTime) |
| `MetricsCheck` | Entered from Soaking | All metric gates + GateTemplates pass (or none configured) |
| `WaitingApproval` | Entered from MetricsCheck | Approval CR with matching Release + target exists |
| `Applying` | Entered from WaitingApproval (or MetricsCheck if no approval required) | `actuator.IsConverged()` returns true |
| `Converged` | Terminal success | — |
| `Failed` | Any gate returns `Failed`, or approval rejected, or timeout | — |

### Failure policy

Configurable per Stage via `GatePolicy.spec.failurePolicy`:

| Policy | Behavior |
|---|---|
| `halt` (default) | Stop entire pipeline on first Sync failure |
| `continue` | Mark Sync failed, continue other Syncs in the stage |
| `retry` | Retry failed Syncs up to `maxRetries` times |

### Automatic rollback `[MVP]`

When `GatePolicy.spec.approval.autoRollback: true` and a Sync fails after reaching `Applying`, the SyncReconciler creates a new **rollback Sync** pointing at the previous artifact version (read from `ReleaseReport.status.environments[].previousVersion`). The rollback Sync is a first-class Sync object — it goes through the full FSM.

### Manual rollback `[MVP]`

```bash
kapro rollback my-release --to sha256:abc123
```

Creates a new `Release` object pointing at the old OCI digest. Fully immutable — the original Release is unchanged.

---

## 11. Notification & Events

### Built-in notification channels `[MVP]`

Configured via `GatePolicy.spec.notifications[]`:

| Channel | Config |
|---|---|
| Slack | `type: slack`, `channel: https://hooks.slack.com/...` |
| Generic webhook | `type: webhook`, `url: https://...` |
| Email (via argoproj/notifications-engine) | `type: email`, `channel: ops@company.com` |
| PagerDuty (via engine) | `type: pagerduty` |
| Teams, OpsGenie, others (via engine) | 15+ providers |

Notifications fire on every FSM transition and on `Failed`. They include one-click approve/reject URLs when entering `WaitingApproval`.

### One-click approval

When a Sync enters `WaitingApproval`, the controller generates HMAC-signed approve and reject URLs using `KAPRO_APPROVAL_SECRET`:

```
POST https://kapro.internal/approve/<token>  → creates Approval CR
POST https://kapro.internal/reject/<token>   → annotates Sync for failure
```

The URLs are embedded in Slack messages, emails, and webhook payloads. Tokens expire after 24 hours.

---

## 12. MVP Scope

### What is in MVP

| Category | What's included |
|---|---|
| **CRDs** | Artifact, Target, Pipeline, Release, GatePolicy, GateTemplate, ManagedCluster, Sync, Approval, ReleaseReport, BootstrapToken |
| **Gate types** | Soak, Metrics (Prometheus), Approval (human), Health (k8s workloads), Verification (cosign), CEL, Job, Webhook |
| **Actuator** | Flux only (`type: flux`) |
| **Provider** | CRD provider (kapro-cluster-controller heartbeat) |
| **Fleet** | ManagedCluster registration + BootstrapToken auth |
| **Notifications** | Slack, Webhook, + argoproj/notifications-engine (15+ channels) |
| **CLI** | `kapro get`, `kapro approve`, `kapro rollback` |
| **Admission** | Mutating (Approval.approvedBy), Validating (Release, Target, Pipeline) |
| **Observability** | Prometheus metrics (`kapro_sync_transitions_total`, `kapro_gate_evaluations_total`, `kapro_stage_duration_seconds`) |
| **Audit** | ReleaseReport per Release |

### What is explicitly cut from MVP

| Category | What's cut | Why |
|---|---|---|
| **Actuators** | ArgoCD, Helm, KServe, Sveltos, OCM | Interface stays; implementations post-MVP |
| **Gates** | KEDA, MLflow, Shadow, KGateway, Argo Analysis, OPA, Plugin | Interface stays; implementations post-MVP |
| **Triggers** | ReleaseTrigger (autonomous MLflow/OCI/Prometheus watches) | Complexity; user creates Release manually in MVP |
| **Plugin system** | PluginGateway, PluginRegistration, gRPC bridge | Too complex for MVP; extensibility via Go interfaces |
| **Providers** | CAPI, OCM, OpenShift, Rancher (direct-connect) | Heartbeat pattern sufficient; no direct network path needed |
| **CDEvents** | CDEvents broker / CloudEvents sink | Post-MVP; webhook notifier covers immediate need |
| **UI** | Web dashboard | CLI + MCP server cover MVP needs |

---

## 13. User Stories

### Platform engineer (primary)

- As a platform engineer, I want to define a `Pipeline` with ordered stages and selector rules so that each release automatically progresses through dev → canary → regional → global prod without manual intervention.
- As a platform engineer, I want to configure a soak period and Prometheus metric thresholds in a `GatePolicy` so that a release cannot advance until it has baked for N hours and error rates are below threshold.
- As a platform engineer, I want to require human approval before production so that a named engineer must explicitly sign off before any cluster receives a release.
- As a platform engineer, I want to roll back a release to a previous OCI digest with a single command so that I can recover from a bad deployment in under 2 minutes without touching GitOps manifests directly.
- As a platform engineer, I want to see the full delivery history for every release — which clusters got which version, when, which gates passed — so that I can audit compliance and diagnose incidents.

### Developer (secondary)

- As a developer, I want to create a `Release` pointing at my OCI digest so that I can trigger the full delivery pipeline without knowing which clusters exist or in what order they should be updated.
- As a developer, I want to receive a Slack notification with an approve/reject link when my release reaches the production approval gate so that I can unblock it from my phone without kubectl access.
- As a developer, I want to see the current phase of my release (`soaking`, `waiting for approval`, `converged`) so that I know whether I need to take action.

### SRE / approver

- As an SRE, I want to approve or reject a pending release with a single click from a Slack message so that I do not need kubectl access to the control plane.
- As an SRE, I want to reject a release and have it automatically annotated with my reason so that the rejection is recorded in the audit trail.

---

## 14. Requirements

### P0 — Must have (MVP cannot ship without these)

- [ ] `Release` creation triggers DAG walk, creates `Sync` per `(Pipeline, Stage, Target)`.
- [ ] `Sync` drives FSM from `Pending` through all configured gates to `Converged` or `Failed`.
- [ ] SoakGate blocks until configured `soakTime` elapses from `Sync.Status.StartedAt`.
- [ ] MetricsGate queries Prometheus and passes only when query returns non-empty vector.
- [ ] ApprovalGate blocks until matching `Approval` CR exists; webhook generates approve/reject URLs.
- [ ] VerificationGate verifies OCI artifact cosign signature before advancing.
- [ ] CELGate evaluates user expression with sync, target, and args context.
- [ ] JobGate creates a Kubernetes Job and polls for exit 0.
- [ ] WebhookGate POSTs to configured URL and interprets JSON response.
- [ ] FluxActuator patches `OCIRepository` and waits for Flux `Kustomization` convergence.
- [ ] `kapro-cluster-controller` writes `ManagedCluster` heartbeat every 30s.
- [ ] `ReleaseReport` aggregates all Syncs into an auditable per-Release record.
- [ ] Admission webhooks prevent creation of invalid `Release` (no artifact, no pipelines) and `Target` (unknown actuator type).
- [ ] Prometheus metrics exported: `kapro_sync_transitions_total`, `kapro_gate_evaluations_total`, `kapro_stage_duration_seconds`.
- [ ] `BuildGateSet(client.Client)` is the single gate construction point; all four built-in gates wired.
- [ ] All gate implementations pass the conformance suite (`conformance/gate.RunSuite`).

### P1 — Should have (high-priority fast follows)

- [ ] `kapro rollback <release> --to <digest>` creates a new Release pointing at prior OCI digest.
- [ ] Automatic rollback: when a gate fails after `Applying`, create rollback Sync automatically.
- [ ] GateTemplate refs in `GatePolicy.spec.gate.templates[]` — parameterised gate chains.
- [ ] argoproj/notifications-engine integration for PagerDuty, Teams, OpsGenie.
- [ ] `KAPRO_CONTROLLERS=*,-releasereport` selective controller enabling.
- [ ] `ReleaseReconciler` halts pipeline stages on `failurePolicy: halt`.
- [ ] Topology-aware stage routing via `Target.spec.topology.accelerator` labels.

### P2 — Future considerations (design must not prevent these)

See `docs/ROADMAP.md` for the full list. The architecture constraints that apply here:
- `ReleaseTrigger`, `PluginGateway`, `PluginRegistration` CRDs are explicitly cut from MVP — do not add them.
- Additional actuators (ArgoCD, Helm, KServe) and gates (KEDA, MLflow, OPA) register via existing `actuator.Registry` / `gate.Registry` — no FSM changes required.
- Cloud direct-connect connectors (GKE, AKS, DigitalOcean, StackIT) register via `provider.Registry` — `ProviderSpec` CRD fields already exist. See ADR-006.
- Multi-tenancy and web dashboard are post-GA concerns.

---

## 15. Success Metrics

### Leading indicators (measurable within 30 days of adoption)

| Metric | Target | Measurement |
|---|---|---|
| Time to first converged release | < 15 min from Release creation to `Converged` on a 3-cluster pipeline | Prometheus `kapro_stage_duration_seconds` |
| Gate pass rate | > 95% of gate evaluations return `Passed` or `Inconclusive` (not error) | `kapro_gate_evaluations_total{result="error"}` < 5% |
| Approval response time | P50 < 10 min, P90 < 2 hours for WaitingApproval phase | `kapro_sync_transitions_total{phase="Applying"}` - `{phase="WaitingApproval"}` delta |
| Cluster registration success | 100% of clusters running kapro-cluster-controller are registered within 5 min | ManagedCluster count vs expected |

### Lagging indicators (measurable at 90 days)

| Metric | Target |
|---|---|
| Production incidents from under-gated promotions | 50% reduction vs baseline |
| Engineering time on manual promotion coordination | 80% reduction (self-reported in quarterly survey) |
| Rollback time | P90 < 5 min from incident detection to rollback Converged |
| Audit compliance | 100% of releases have a complete ReleaseReport with gate pass evidence |

---

## 16. Non-Goals

**Not a GitOps tool.** Kapro does not manage Git repositories, write commits, or replace Flux/ArgoCD. It tells Flux/ArgoCD *when* to apply a version — the actual GitOps delivery is delegated.

**Not a CI system.** Kapro does not build images, run tests, or create OCI artifacts. It assumes the artifact already exists at a known digest. CI pipelines create the `Artifact` CR after pushing to a registry.

**Not a service mesh.** Kapro does not manage traffic splitting, canary weights, or circuit breakers directly. These can be gated via the Webhook or CEL gate querying mesh APIs.

**Not a secret manager.** Kapro reads credentials from Kubernetes Secrets (cosign keys, Prometheus auth, notification tokens). It does not manage secret rotation or storage.

**Not multi-tenant in MVP.** All Kapro CRDs in a namespace are visible to all controllers. RBAC isolation per team is a post-MVP concern.

**Not a UI.** The CLI and MCP server cover MVP observability. A web dashboard is explicitly post-MVP.

---

## 17. Open Questions

| Question | Owner | Blocking? |
|---|---|---|
| What is the correct `Approval` CR GC policy — delete on `Converged`, or keep for audit? | Engineering | No |
| Should `ReleaseReport` be writable by users (for annotations) or fully system-managed? | Product | No |
| Rate limit for the approval webhook server — should it have HMAC replay protection beyond token expiry? | Security | No |
| How should `Stage.clusterSelector` handle zero matches — fail the stage or skip it? | Product | No |
| Should `kapro-cluster-controller` support push-based health reporting (events) vs poll-based (heartbeat interval)? | **Decided: push-based HTTP POST `/heartbeat` every 30s** (NameNode/DataNode pattern). Spoke POSTs to hub; hub never dials spoke. | Closed ✅ |
| What is the retention policy for `Sync` objects — archive to `ReleaseReport` then delete? | Engineering | No |

---

## Appendix A — Key Design Decisions

### A1. Why Sync, not Promotion?

`Promotion` implies one-directional movement. `Sync` is the precise Kubernetes term: reconcile the actual state of an target to match the desired state declared in a `Release`. A `Sync` may promote, rollback, or re-apply — it is directional by the artifact version in the `Release`, not by the word.

### A2. Why CRD provider as default, with optional direct-connect?

Requiring the control plane to have network access to every spoke cluster is an unrealistic operational constraint for air-gapped, multi-cloud, or enterprise environments. The heartbeat pattern (spoke writes to hub) inverts the trust model: spoke clusters only need outbound HTTPS to the hub. No VPN, no peering, no firewall holes.

Cloud-native direct-connect providers (GKE, EKS, AKS, DigitalOcean, StackIT) are additive: they use cloud IAM (Workload Identity, IRSA, Managed Identity) to eliminate the cluster-controller agent for teams that prefer zero-agent operation. Both paths coexist per-target. See ADR-006 for the full analysis and auth matrix.

### A3. Why immutable Releases?

Mutable release objects create audit ambiguity: "was this the Release that caused the incident, or was it modified after?" Immutability makes the audit trail append-only. Rollback is expressed as a new Release — the record is complete and tamper-evident.

### A4. Why gate.Gate is stateless?

Gates that carry state in memory cannot survive controller restarts. All gate run state lives on `Sync.Status.Gates[]` in etcd. The controller re-derives gate state on every reconcile from these persisted records. This is the same principle Kubernetes uses for container status — the kubelet re-reads cgroup state on restart, not memory.

### A5. Why BuildGateSet takes a client.Client?

The previous design called `BuildGateSet()` with no arguments, then manually set `gates.Approval` and `gates.Verification` in `main.go`. This created an asymmetric construction pattern where `BuildGateSet` returned a half-wired struct. Passing `client.Client` into `BuildGateSet` makes it the single, complete construction point — a function that takes its dependencies and returns a fully operational value.

### A6. Why two provider paths (CRD + direct-connect) instead of one?

The CRD provider is non-negotiable for air-gapped and on-prem deployments — it requires only outbound HTTPS from spoke to hub, which is achievable in almost any network topology. Direct-connect providers (GKE, EKS, AKS, etc.) require hub-to-spoke network access and cloud IAM bindings, which are richer but not universally available.

Making both paths available and selecting them per-target via `spec.provider.type` means operators are not forced to compromise: air-gapped clusters use Path A; cloud-managed clusters can optionally use Path B for zero-agent operation. The `provider.Registry` pattern (mirroring `actuator.Registry`) provides the runtime dispatch with no changes to the FSM. See ADR-006 for full analysis, auth matrix, and per-cloud bootstrap flows.

---

## Appendix B — Architecture Quality (v0.2)

Full review: `docs/ARCHITECTURE_REVIEW.md`

### KXI interface grades

| Interface | Score | Notes |
|-----------|-------|-------|
| KGI (`pkg/gate`) | 10/10 | `Result.Phase` authoritative; `Result.Passed` deprecated (removal tracked in ROADMAP.md); `IsPassed/IsInconclusive/NormalisePhase` helpers |
| KAI (`pkg/actuator`) | 10/10 | `IsConverged(ctx, env, version, appKey)` — explicit and symmetric with `Apply(ApplyRequest{AppKey})`; `resolveAppKey("default")` fallback |
| KCI (`pkg/provider`) | 10/10 | `provider.Registry`, `ProviderSpec` with GKE/AKS/DO/StackIT, `NopConnector` (fails loudly), two-path CRD+direct model; Path B connectors tracked in ROADMAP.md |

All 7 architecture gaps identified in the initial review (G1–G7) have been resolved in v0.2. All KXI interfaces score 10/10. The codebase is gap-free at the interface layer.

### Completed in v0.2 extensibility pass

| Item | Status |
|------|--------|
| `KNI`: `Notifier.Notify(*GatePolicy)` → `Notifier.Notify(NotificationPolicy)` — zero CRD coupling | ✅ Done |
| `KGI`: `GateRegistry` — open extension point; `gateForTemplate` switch replaced with registry lookup | ✅ Done |
| `KGI`: `gate.Registry` — third registry alongside `actuator.Registry` and `provider.Registry` | ✅ Done |
| `KCI`: `internal/provider/gke` — GKE Connector (Workload Identity, keyless); registered as `"gke"` in `cmd/operator/main.go` | ✅ Interface done; implementation moved to ROADMAP.md v0.3 |
| Heartbeat architecture: `POST /heartbeat` endpoint on hub; spoke sends `HeartbeatRequest`, hub patches `ManagedCluster.status`, responds with `HeartbeatResponse` | ✅ Done |
| cluster-controller refactored: removed hub K8s API calls; HTTP-only communication; SA Bearer token via K8s TokenReview | ✅ Done |
| Build fixed: `zz_generated.deepcopy.go` — 4 stale field references removed; `go build ./...` clean | ✅ Done |
| All tests pass: `go test ./...` — 6 packages, 0 failures | ✅ Done |

Planned implementation work is tracked in `docs/ROADMAP.md`.

---

*Last updated: 2026-04-19. All KXI interfaces 10/10. G1–G7 gaps closed. Heartbeat architecture (NameNode/DataNode pattern) implemented. Path A canonical for all cluster topologies. EKS removed (ROADMAP.md). Build clean (Go 1.25, controller-gen v0.17.0). All tests pass. Freeze: Flux + CRD provider + CEL gate + full interface layer + HTTP heartbeat.*
