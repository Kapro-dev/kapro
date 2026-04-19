# ADR-007: KXI — The Kapro Extension Interface Family

**Status:** Accepted  
**Date:** 2026-04-19  
**Deciders:** Kapro maintainers  
**Supersedes:** N/A  
**Extends:** ADR-001 through ADR-006

---

## Context

Kubernetes became the de facto compute orchestration platform not by being the fastest or the simplest, but by being the most *extensible*. The CRI (Container Runtime Interface), CNI (Container Network Interface), CSI (Container Storage Interface), CCM (Cloud Controller Manager Interface), and Device Plugin API created a stable, versioned, conformance-tested extension surface. Any runtime, any network, any storage, any cloud could integrate without forking the core. This is why Kubernetes runs equally well on a Raspberry Pi, an AWS bare-metal instance, and a supercomputer.

Kapro has the same opportunity in the progressive delivery space. The core insight: **progressive delivery is a composition of pluggable subsystems**, not a monolithic pipeline. Every subsystem that touches an external system — cluster connectivity, artifact delivery, gate evaluation, health assessment, notification dispatch, OCI interaction, signature verification — is an extension point that can and should have a formal, versioned, conformance-tested interface.

Today, Kapro has seven distinct interfaces scattered across `pkg/`. They are documented individually, without a unifying naming scheme, without cross-referenced conformance guarantees, and without a shared design philosophy. The `actuator.Registry` exists; the `provider.Registry` did not. `KHI.AssessRequest.KubeConfig` was `interface{}`. The interfaces are good but not *systematic*.

This ADR establishes **KXI — the Kapro Extension Interface family** as the canonical architecture for all Kapro extension points, modelled on how Kubernetes designed CRI/CNI/CSI.

---

## Decision

### The KXI Design Axioms

Every KXI interface follows six invariants. These are non-negotiable:

1. **One interface, one question.** KGI answers "is it safe to advance?" KAI answers "apply this version." KCI answers "how do I reach this cluster?" Interfaces that answer two questions are split.

2. **Stateless implementations.** State lives in etcd (Sync.Status, ManagedCluster.Status). Implementations carry only configuration, never runtime state. This means implementations survive controller restarts and can be called from multiple goroutines without coordination.

3. **Concurrent-safety is documented and required.** Every interface godoc states "Implementations must be safe for concurrent use." This is not optional.

4. **Nop implementations for everything.** Every KXI interface ships a `Nop*` implementation in the same package. Nop implementations make a deliberate choice: they either pass-through (safe no-op for testing) or return an explicit error that tells the operator *why* a Nop was reached (configuration bug detection). Never silent data loss.

5. **Compile-time registration checks.** Every concrete implementation has `var _ InterfaceType = (*ConcreteType)(nil)`. This catches interface drift at compile time, not at runtime when a customer hits the code path.

6. **Conformance suite in `conformance/`.** Every KXI interface has a `conformance/X/suite.go` with a `RunSuite(t, impl)` function. A new implementation is not production-ready until it passes conformance. This is how Kubernetes enforces CRI compatibility — Kapro does the same.

---

### The KXI Family (v1alpha1)

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Kapro Extension Interfaces                      │
├────────┬─────────────────────────┬──────────────────────────────────┤
│ Abbrev │ Interface               │ Question answered                │
├────────┼─────────────────────────┼──────────────────────────────────┤
│ KGI    │ gate.Gate               │ Is it safe to advance this Sync? │
│ KAI    │ actuator.Actuator       │ Apply / converge / rollback      │
│ KCI    │ provider.Connector      │ How do I connect to this cluster?│
│        │ provider.RegistrationR. │ What did the cluster report?     │
│ KNI    │ notification.Notifier   │ Fan out this lifecycle event     │
│ KHI    │ health.Assessor         │ Are workloads healthy?           │
│ KRI    │ oci.Service             │ Inspect / promote OCI artifacts  │
│ KVI    │ verification.Verifier   │ Is this artifact signature valid?│
├────────┼─────────────────────────┼──────────────────────────────────┤
│ KSI    │ scheduler.Plugin        │ Which stage runs next? (v0.2)    │
└────────┴─────────────────────────┴──────────────────────────────────┘
```

The Kubernetes parallel:

```
┌──────────────────────────────────────────────────────────────────┐
│            KXI ↔ Kubernetes Interface Analogy                    │
├─────────┬────────────────────────┬────────────────────────────── ┤
│ KXI     │ Kubernetes equivalent  │ Why the analogy holds         │
├─────────┼────────────────────────┼───────────────────────────────┤
│ KGI     │ Admission Webhook      │ Both gate passage of requests │
│ KAI     │ CRI                    │ Both delegate "run this"      │
│ KCI     │ CCM (cloud-controller) │ Both abstract cloud topology  │
│ KNI     │ Event sink / audit log │ Both fan out lifecycle events │
│ KHI     │ Readiness probe        │ Both assess workload health   │
│ KRI     │ Container image pull   │ Both fetch immutable content  │
│ KVI     │ Image signature verify │ Both verify artifact integrity│
│ KSI     │ Scheduler Plugin Fwk   │ Both decide "what runs next"  │
└─────────┴────────────────────────┴───────────────────────────────┘
```

---

### The Generic Registry

All KXI registries use a single generic foundation:

```go
// pkg/registry/registry.go
type Registry[T any] struct { ... }

func New[T any](registryName string) *Registry[T]
func (r *Registry[T]) Register(typeName string, impl T) error
func (r *Registry[T]) MustRegister(typeName string, impl T)
func (r *Registry[T]) Resolve(typeName string) (T, error)
func (r *Registry[T]) Has(typeName string) bool
func (r *Registry[T]) Names() []string
func (r *Registry[T]) All() map[string]T
func (r *Registry[T]) Unregister(typeName string) bool
func (r *Registry[T]) Len() int
```

Per-type registries embed `*Registry[T]`:

```go
// pkg/actuator/registry.go
type Registry struct { *pkgregistry.Registry[Actuator] }
func NewRegistry() *Registry { return &Registry{pkgregistry.New[Actuator]("actuator")} }

// pkg/provider/registry.go
type Registry struct { *pkgregistry.Registry[Connector] }
func NewRegistry() *Registry { return &Registry{pkgregistry.New[Connector]("provider")} }
```

This eliminates the duplicated `sync.RWMutex + map` pattern that previously existed separately in `actuator.Registry`. New KXI registries (KNI registry for multi-notifier dispatch, KHI registry for assessor composition) are trivially added.

---

### KGI — Kapro Gate Interface

```go
// pkg/gate/gate.go
type Gate interface {
    Evaluate(ctx context.Context, req Request) (Result, error)
}
```

**Contract:**
- Stateless: all gate run state lives in `Sync.Status.Gates[]`
- Idempotent: calling Evaluate twice returns the same result for the same state
- Concurrent-safe

**Result.Phase supersedes Result.Passed** (deprecation path):
- v0.1: both fields populated for backwards compat
- v0.2: `Result.Passed` deprecated with godoc warning
- v0.3: `Result.Passed` removed; only `Result.Phase` used

**CEL variables**: `args.*`, `environment.*`, `sync.*` (not `promotion.*`)

**Dispatch**: Type-switched by `GateTemplate.spec.type` in `gateForTemplate()`. Built-in: `cel`, `job`, `webhook`. Registered at startup.

**Conformance**: `conformance/gate/suite.go:RunSuite(t, impl)`

---

### KAI — Kapro Actuator Interface

```go
// pkg/actuator/actuator.go
type Actuator interface {
    Apply(ctx context.Context, req ApplyRequest) error
    IsConverged(ctx context.Context, env *v1alpha1.Environment, version string) (bool, error)
    Rollback(ctx context.Context, env *v1alpha1.Environment, previousVersion string) error
}
```

**Contract:**
- `Apply` is idempotent: calling twice with the same version is safe
- `IsConverged` never blocks: returns immediately with current state
- `Rollback` is `Apply` with the previous version — same idempotence guarantee
- Concurrent-safe

**v0.2 done**: `AppKey string` added to `IsConverged` — signature is now `IsConverged(ctx, env, version, appKey string) (bool, error)`. Callers pass `syncAppKey(sync)` explicitly; implementations no longer need to read it from `ManagedCluster.spec.desiredAppKey`.

**Registry dispatch**: `env.Spec.Actuator.Type → actuator.Registry.Resolve(type)`

**Registered types**: `flux` (MVP). `argocd`, `helm` (v0.3).

**Conformance**: `conformance/actuator/suite.go:RunSuite(t, impl)` — *to be created in v0.2*

---

### KCI — Kapro Cluster Interface

KCI is deliberately split into two sub-interfaces matching the two onboarding paths:

```go
// pkg/provider/provider.go

// KCI-Connect: hub → spoke direct connection
type Connector interface {
    Connect(ctx context.Context, env *v1alpha1.Environment) (*rest.Config, error)
    IsReachable(ctx context.Context, env *v1alpha1.Environment) (bool, error)
}

// KCI-Register: hub reads cluster state from CRDs (no network path needed)
type RegistrationReader interface {
    GetRegistration(ctx context.Context, env *v1alpha1.Environment) (*v1alpha1.ManagedCluster, error)
}
```

**Dispatch logic** in the SyncReconciler:

```
if provider.IsCRDPath(env.Spec.Provider.Type):
    → CRDProvider.GetRegistration()   (RegistrationReader path, default)
else:
    → ProviderRegistry.Resolve(type)  (Connector path, v0.3+)
```

**Why split?** The two interfaces answer different questions. `Connector` answers "give me a `*rest.Config` now" — it's network-active, cloud-IAM-dependent, may block. `RegistrationReader` answers "what did the cluster-controller report?" — it's a local etcd read, never blocks, works air-gapped. Merging them into one interface would force every RegistrationReader to implement `Connect()` as a stub, which violates Axiom 1 (one interface, one question).

**Connector implementations** (v0.3+, one per cloud):

```
internal/provider/
├── crd/         ← RegistrationReader (MVP, already shipped)
├── gke/         ← Connector: Workload Identity + GKE Connect Gateway
├── eks/         ← Connector: IRSA + STS AssumeRoleWithWebIdentity
├── aks/         ← Connector: Managed Identity + AAD OIDC
├── digitalocean/← Connector: API v2 token (Secret-referenced)
└── stackit/     ← Connector: Service Account key (Secret-referenced)
```

**Auth matrix:**

```
Provider    | Keyless IAM              | Static secret?
────────────┼──────────────────────────┼───────────────
gke         | Workload Identity        | No
eks         | IRSA + STS              | No
aks         | Managed Identity + OIDC  | No
digitalocean| ✗                        | Yes (API token)
stackit     | Planned (2026)           | Yes (SA key)
on-prem     | HMAC bootstrap only      | No
```

**Conformance**: `conformance/provider/suite.go:RunSuite(t, impl)` ✅ (already exists)

---

### KCI — ProviderSpec (CRD surface)

```go
// api/v1alpha1/types.go

type ProviderSpec struct {
    Type         string                   `json:"type,omitempty"`
    GKE          *GKEProviderSpec         `json:"gke,omitempty"`
    EKS          *EKSProviderSpec         `json:"eks,omitempty"`
    AKS          *AKSProviderSpec         `json:"aks,omitempty"`
    DigitalOcean *DigitalOceanProviderSpec `json:"digitalOcean,omitempty"`
    StackIT      *StackITProviderSpec     `json:"stackit,omitempty"`
}
```

Security invariant: **all credential references are by Secret name only**. No cloud credentials appear in CRD fields. Fields like `TokenSecretRef` and `ServiceAccountKeySecretRef` always reference a `Secret` in `kapro-system`, not inline values.

---

### KCI — ClusterCapabilities (extended)

```go
type ClusterCapabilities struct {
    // Software
    K8sVersion     string
    FluxVersion    string
    ArgoCDVersion  string
    SveltosVersion string
    NodeCount      int

    // Cloud identity (new in v0.1.1)
    Cloud      string  // gcp | aws | azure | digitalocean | stackit | on-prem
    Region     string  // europe-west1 | us-east-1 | westeurope
    Zone       string  // europe-west1-b | us-east-1a | 1
    AccountID  string  // GCP project | AWS account | Azure subscription | ...
    ClusterID  string  // cloud-provider cluster identifier
}
```

Cloud metadata enables:
- **Cloud-aware stage selectors**: `matchLabels: kapro.io/cloud: gcp`
- **Compliance routing**: EU-sovereign workloads stay in `Region: eu01` (StackIT)
- **Cost attribution**: `AccountID` maps to cloud billing account
- **Audit trails**: every delivery event carries cloud provenance

---

### KNI — Kapro Notification Interface

```go
// pkg/notification/notifier.go
type Notifier interface {
    Notify(ctx context.Context, event Event, policy *v1alpha1.GatePolicy)
}
```

**Contract:**
- `Notify` MUST NOT block — fan-out must be async or fire-and-forget
- `Notify` MUST NOT return an error — log internally and continue
- Concurrent-safe

**v0.2 improvement**: Decouple `Event` from `*v1alpha1.GatePolicy`. Pass a `NotificationContext` struct that carries only what notifiers need (channel refs, severity, URLs), not the full GatePolicy CRD object. This removes the import of `kapro.io/kapro/api/v1alpha1` from `pkg/notification`, making it safe to use in external plugins without pulling in all CRD types.

**Registered implementations**:
- `internal/notification/notifier.go` — Slack + Webhook (zero deps)
- `internal/notification/engine/` — argoproj/notifications-engine (15+ providers)

---

### KHI — Kapro Health Interface

```go
// pkg/health/health.go
type Assessor interface {
    AssessHealth(ctx context.Context, req AssessRequest) (AssessResult, error)
}

type AssessRequest struct {
    Namespace  string
    Kinds      []string
    KubeConfig *rest.Config  // nil on CRD path; non-nil on direct-connect path
}
```

**Breaking change from v0.1.0**: `KubeConfig` was previously typed as `interface{}`. Changed to `*rest.Config` (the concrete type it has always carried) to restore compile-time type safety. The comment "implementations must type-assert to *rest.Config" was a lie in the interface godoc — it should never have been `interface{}`.

**Contract:**
- Read-only: `AssessHealth` MUST NOT mutate cluster state
- `KubeConfig == nil` means health data comes from `ManagedCluster.status.health`
- Concurrent-safe

**Nop**: `NopAssessor` returns `StatusHealthy` for all requests (added)

---

### KRI — Kapro Registry Interface

```go
// pkg/oci/oci.go (note: package name is "oci", interface is KRI)
type Service interface {
    Exists(ctx, repo, reference string) (bool, error)
    Inspect(ctx, repo, reference string) (*ArtifactInfo, error)
    Tag(ctx, repo, srcDigest, newTag string) error
    Copy(ctx, srcRepo, srcRef, dstRepo, dstTag string) error
    ListTags(ctx, repo string) ([]string, error)
}
```

**Contract:**
- `Copy` is idempotent: copying the same digest to the same destination tag is safe
- Concurrent-safe
- `NopOCIService` already exists ✅

**v0.2**: Add `Delete(ctx, repo, reference string) error` for post-delivery cleanup policies.

---

### KVI — Kapro Verification Interface

```go
// pkg/verification/verification.go
type Verifier interface {
    Verify(ctx context.Context, req VerifyRequest) (VerifyResult, error)
}
```

**Contract:**
- `Verify` is deterministic for a given `(ImageRef, key/keyless config)` pair
- `Verify` MUST respect `ctx.Done()` — do not block indefinitely on Rekor calls
- Concurrent-safe
- `NopVerifier` already exists ✅

---

### KSI — Kapro Scheduling Interface (v0.2)

```go
// pkg/scheduler/plugin.go (planned)

// FilterPlugin decides whether a stage is eligible to run now.
type FilterPlugin interface {
    Filter(ctx context.Context, state *CycleState, release *v1alpha1.Release, stage *v1alpha1.Stage) *Status
}

// ScorePlugin ranks eligible stages (for parallel stage waves).
type ScorePlugin interface {
    Score(ctx context.Context, state *CycleState, release *v1alpha1.Release, stage *v1alpha1.Stage) (int64, *Status)
}

// BindPlugin creates the Sync object (the "bind" step in kube-scheduler terms).
type BindPlugin interface {
    Bind(ctx context.Context, state *CycleState, release *v1alpha1.Release, stage *v1alpha1.Stage) *Status
}
```

Modelled directly on the [Kubernetes Scheduler Plugin Framework](https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/). The `PipelineController` becomes a scheduling loop:

```
for each unscheduled stage:
    1. Filter:  FilterPlugins.Filter(state, release, stage)  → eligible?
    2. Score:   ScorePlugins.Score(state, release, stage)    → priority
    3. Bind:    BindPlugins.Bind(state, release, stage)      → create Sync
```

This replaces the current ad-hoc DAG walk in `ReleaseReconciler` with a formally pluggable framework. External scheduling plugins (e.g. "only promote to GPU clusters on weekdays", "never promote to prod during US peak hours") implement `FilterPlugin` and register at startup.

---

### The Extension Lifecycle

```
                    ┌─────────────────────┐
                    │  Implement KXI      │
                    │  interface          │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  Pass conformance   │
                    │  suite              │  RunSuite(t, &MyImpl{})
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  Register at        │
                    │  startup            │  reg.MustRegister("mytype", &MyImpl{})
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  Runtime dispatch   │
                    │  by type name       │  reg.Resolve(env.Spec.Actuator.Type)
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  Retire             │
                    │  (if needed)        │  reg.Unregister("mytype")
                    └─────────────────────┘
```

---

### ControllerContext as dependency bundle

`ControllerContext` is the dependency injection container for all controllers.
After this ADR, it carries:

```go
type ControllerContext struct {
    Manager          ctrl.Manager
    Recorder         record.EventRecorder
    ActuatorRegistry *actuator.Registry   // KAI dispatch
    ProviderRegistry *provider.Registry   // KCI Connector dispatch (v0.3+)
    Gates            GateSet              // KGI built-in gates
    HealthAssessor   health.Assessor      // KHI
    Notifier         notification.Notifier // KNI
    OCIService       oci.Service          // KRI
    ApprovalSecret   []byte
    ExternalURL      string
}
```

`ProviderRegistry` is added as a non-breaking field. In MVP it is empty (all environments use the CRD path). In v0.3 it is populated with GKE and EKS connectors.

---

## Options Considered

### Option A: Ad-hoc per-interface registries (status quo)

Each interface manages its own dispatch independently. `actuator.Registry` has its own mutex+map. `provider.Registry` would have its own. No shared foundation.

| Dimension | Assessment |
|-----------|------------|
| Complexity | Low — each registry is self-contained |
| Code duplication | High — mutex+map+error messages repeated |
| Consistency | Low — each registry has slightly different error messages and behaviors |
| Extensibility | Medium — new registries require new boilerplate |

**Cons:** The `actuator.Registry` had `keys()` (unexported) while the generic has `Names()` (exported). The inconsistency accumulates as more registries are added.

### Option B: Generic Registry[T] with per-type wrappers (chosen)

One `pkg/registry.Registry[T]` implementation. Per-type registries embed it.

| Dimension | Assessment |
|-----------|------------|
| Complexity | Low — one generic, thin wrappers |
| Code duplication | None — lock, map, error messages defined once |
| Consistency | High — all registries behave identically |
| Extensibility | High — new registry is 10 lines |
| Go version | Requires Go 1.18+ (generics) — Kapro uses 1.25 ✅ |

**Cons:** Slightly more indirection to read. Acceptable trade-off.

### Option C: Interface-based dispatch with reflection

Use `reflect` to dispatch by interface type at runtime. No registries.

| Dimension | Assessment |
|-----------|------------|
| Complexity | High — reflection is hard to read and debug |
| Type safety | Low — errors at runtime, not compile time |

Rejected. Kapro's design philosophy is "catch errors at compile time."

---

## Trade-off Analysis

The KXI family is not new functionality — every interface already exists. This ADR is about **standardising what already works into a form that scales**. The cost is low (< 2 days of refactoring). The benefit is high: every new engineer who joins reads the KXI table and immediately understands all extension points, their analogues in Kubernetes, and how to implement one.

The generic registry adds ~150 lines of carefully documented Go. It eliminates ~50 lines per future registry that would otherwise be written by copy-paste. Break-even at 2 registries; we already have 2 (actuator, provider), with 2 more planned (notifier composition, scheduler plugins).

---

## Consequences

**What becomes easier:**
- Adding a new cloud provider: implement `kci.Connector`, pass `RunSuite`, register. Zero changes to the FSM.
- Adding a new actuator type: implement `kai.Actuator`, pass conformance, register. Zero changes to the scheduler.
- Onboarding a new engineer: read the KXI table, pick a subsystem, implement the interface, pass conformance.
- Testing: every KXI interface has a `Nop*` — no mocking frameworks needed for unit tests.

**What becomes harder:**
- The `actuator.Registry` API surface changed from a struct with hand-written methods to a struct that embeds `*Registry[Actuator]`. Callers that used `r.keys()` (unexported) will compile-error — but `keys()` was unexported so no external caller could use it.

**What we'll need to revisit:**
- `KNI`: remove `*v1alpha1.GatePolicy` from `Notifier.Notify` — replace with a `NotificationContext` value type in v0.2.
- ~~`KAI.IsConverged`: add explicit `appKey string` parameter in v0.2.~~ ✅ **Done in v0.2**
- ~~`KGI.Result.Passed`: deprecate in v0.2, remove in v0.3.~~ ✅ **Done in v0.2** — `IsPassed()`, `IsInconclusive()`, `NormalisePhase()` helpers added
- `KSI`: design and implement the scheduling plugin framework in v0.2.

---

## Action Items

- [x] `pkg/registry/registry.go` — generic Registry[T] (DONE)
- [x] `pkg/actuator/registry.go` — embed generic registry (DONE)
- [x] `pkg/provider/registry.go` — new, embed generic registry (DONE)
- [x] `pkg/provider/provider.go` — add NopConnector, improve godoc (DONE)
- [x] `pkg/health/health.go` — fix `interface{}` → `*rest.Config`, add NopAssessor (DONE)
- [x] `api/v1alpha1/types.go` — extend ProviderSpec with cloud fields (DONE)
- [x] `api/v1alpha1/types.go` — extend ClusterCapabilities with cloud metadata (DONE)
- [x] `api/v1alpha1/zz_generated.deepcopy.go` — add deepcopy for 5 new provider spec types (DONE)
- [x] `internal/provider/crd/crd_provider.go` — fix `CurrentVersion()` hardcoded "ocs" appKey (DONE)
- [x] `pkg/controllermanager/controllermanager.go` — add `ProviderRegistry *provider.Registry` to ControllerContext (DONE)
- [x] `pkg/controllermanager/controllers.go` — add CEL to BuildGateSet; all 5 gates symmetric (DONE — G6)
- [x] `pkg/gate/gate.go` — `Result.Passed` deprecated; `IsPassed/IsInconclusive/NormalisePhase` helpers (DONE — G5)
- [x] `pkg/actuator/actuator.go` — `IsConverged(ctx, env, version, appKey)` explicit param (DONE — G7)
- [x] `conformance/actuator/suite.go` — KAI conformance suite created (DONE)
- [ ] `conformance/notifier/suite.go` — create KNI conformance suite (v0.2)
- [ ] `conformance/health/suite.go` — create KHI conformance suite (v0.2)
- [ ] `pkg/scheduler/plugin.go` — define KSI (v0.2)
- [ ] `internal/provider/gke/` — implement KCI Connector for GKE (v0.3)
- [ ] `internal/provider/eks/` — implement KCI Connector for EKS (v0.3)
- [ ] `internal/provider/aks/` — implement KCI Connector for AKS (v0.4)
- [ ] `internal/provider/digitalocean/` — implement KCI Connector for DigitalOcean (v0.4)
- [ ] `internal/provider/stackit/` — implement KCI Connector for StackIT (v0.4)

---

## Appendix: KXI Quick Reference for Contributors

```
I want to...                          | I implement...  | I register in...
──────────────────────────────────────┼─────────────────┼──────────────────────
Add a new gate type                   | gate.Gate       | gateForTemplate()
Add a new delivery backend            | actuator.Actuator| actuator.Registry
Add a new cloud provider (direct)     | provider.Connector| provider.Registry
Add a new cloud provider (outbound)   | Deploy kcc agent | BootstrapToken flow
Add a new notification channel        | notification.Notifier| ControllerContext
Add a new health assessor             | health.Assessor  | ControllerContext
Add a new OCI registry backend        | oci.Service      | ControllerContext
Add a new signature verifier          | verification.Verifier| VerificationGate
Add a new scheduling policy (v0.2)    | scheduler.FilterPlugin| SchedulerRegistry
```
