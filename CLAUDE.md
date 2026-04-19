# Memory

## This Project
**Kapro** — Kubernetes-native progressive delivery orchestrator for cluster fleets.
Sits above GitOps tools (Flux/ArgoCD); decides *when* and *in what order* environments receive versions.

> **Canonical spec:** `docs/SPEC.md` — always read this first for any architectural question.
> Update it whenever CRDs, gate types, MVP scope, or ADRs change.

## Key Terms
| Term | Meaning |
|------|---------|
| **Sync** | One delivery attempt of a version to one environment (FSM unit) |
| **Release** | Immutable record of "version X should flow through pipeline Y" |
| **Pipeline** | Ordered DAG of stages; each stage targets an Environment |
| **Environment** | Logical target (cluster + namespace + actuator config) |
| **Gate** | Stateless evaluator → `Result{Passed,Inconclusive,Failed}` |
| **Actuator** | Applies a version to a cluster; MVP = FluxActuator only |
| **GatePolicy** | Per-environment gate config (which gates, what args) |
| **GateTemplate** | Reusable parameterised gate (`cel \| job \| webhook`) |
| **ManagedCluster** | Hub-side CRD; heartbeat from spoke cluster-controller |
| **BootstrapToken** | Short-lived HMAC token for first cluster registration |
| **ReleaseReport** | System-generated audit record aggregating all Syncs per Release |
| **Approval** | User-created CRD to unblock WaitingApproval gate |
| **CRD provider** | MVP fleet topology; spoke writes ManagedCluster to hub |
| **Pod analogy** | Environment=Pod, ManagedCluster=Node, Sync=container run |
→ Full glossary: `memory/glossary.md`

## MVP CRDs (11)
User-facing: `Artifact, Environment, Pipeline, Release, GatePolicy, GateTemplate, ManagedCluster`
System: `Sync, Approval, ReleaseReport, BootstrapToken`
Cut: `ReleaseTrigger, PluginGateway, PluginRegistration`

## Sync FSM Phases
`Pending → Verification → HealthCheck → Soaking → MetricsCheck → WaitingApproval → Applying → Converged | Failed`

## Gate Interface
```go
type Gate interface {
    Evaluate(ctx context.Context, req Request) (Result, error)
}
// Request.Sync (not Promotion — renamed)
// Result.Phase: Passed | Inconclusive | Failed
```

## Actuator Interface
```go
type Actuator interface {
    Apply(ctx context.Context, req ApplyRequest) error
    // v0.2: appKey explicit — key in ManagedCluster.status.currentVersions
    IsConverged(ctx context.Context, env *Environment, version, appKey string) (bool, error)
    Rollback(ctx context.Context, env *Environment, previousVersion string) error
}
```

## BuildGateSet
```go
func BuildGateSet(c client.Client) GateSet  // all 5 FSM-phase gates (Soak, Metrics, Approval, Verification, CEL)
```

## BuildGateRegistry
```go
func BuildGateRegistry(c client.Client) *gate.Registry
// registers: "cel" | "job" | "webhook"
// external types: cc.GateRegistry.MustRegister("argo-analysis", impl)
// gateForTemplate uses registry when non-nil; falls back to switch for tests
```

## KNI Decoupling
```go
// pkg/notification has ZERO import of api/v1alpha1
// SyncReconciler calls notificationPolicyFrom(*GatePolicy) → NotificationPolicy
// before every Notifier.Notify() call
type NotificationPolicy struct { Channels []Channel }
type Channel struct { Type, Target string; Email *EmailConfig }
```

## CEL Variables
`args.*`, `environment.*`, `sync.*`  (not `promotion.*`)

## Key Files
| File | Purpose |
|------|---------|
| `docs/SPEC.md` | **Living spec — update this as things change** |
| `api/v1alpha1/types.go` | All CRD type definitions |
| `api/v1alpha1/zz_generated.deepcopy.go` | Manual deepcopy (no codegen in sandbox) |
| `internal/controller/sync_controller.go` | Main FSM reconciler |
| `pkg/controllermanager/controllers.go` | Gate wiring + controller registration |
| `pkg/controllermanager/controllermanager.go` | GateSet struct |
| `pkg/gate/gate.go` | Gate interface |
| `pkg/actuator/actuator.go` | Actuator interface |
| `internal/gate/cel/cel_gate.go` | CEL gate (var: "sync" not "promotion") |
| `internal/provider/gke/gke_connector.go` | GKE Connector (KCI, Workload Identity) |
| `internal/provider/crd/crd_provider.go` | CRD Provider (KCI, Path A, all clouds) |
| `cmd/operator/main.go` | Binary entry; registers FluxActuator + GKEConnector |

## Architecture Rules
- `SyncReconciler` fields use `gate.Gate` interface — never concrete types
- `BuildGateSet()` returns a complete GateSet — **all 5 gates** including CEL; never half-wired
- All gate state lives in `Sync.Status.Gates[]` in etcd — gates are stateless
- `Result.Phase` is authoritative (v0.2); `Result.Passed` deprecated → removed in v0.3
- `IsConverged(ctx, env, version, appKey)` — always pass appKey explicitly (v0.2+)
- Rollback = new Release pointing at old OCI digest — never mutate a Release
- Metrics subsystem: `"sync"` (not `"promotion"`)

## All G1–G7 Architecture Gaps: CLOSED ✅
See `docs/SPEC.md` Appendix B for full closure summary.

## Freeze Status
The following is the frozen, production-quality implementation for developer onboarding:
- **Actuator**: FluxActuator (`internal/actuator/flux/`) — registered as `"flux"`
- **Provider (Path A)**: CRDProvider (`internal/provider/crd/`) — default for all clouds
- **Provider (Path B)**: GKEConnector (`internal/provider/gke/`) — registered as `"gke"`, Workload Identity keyless
- **Gate (showcase)**: CELGate (`internal/gate/cel/`) — most expressive, demonstrates the extension model
- **All interfaces**: KGI, KAI, KCI, KNI — all 10/10, all conformance-tested
- Everything else in ROADMAP.md. Do NOT add new patterns — follow the existing ones.

## Roadmap
| Version | Theme |
|---------|-------|
| v0.1 | MVP: single-hub, Flux only, 4 gates |
| v0.2 | Architecture hardening: KXI interfaces, G1–G7 closed, 5 cloud providers |
| v0.3 | ArgoCD actuator + GKE/EKS/AKS cloud connectors |
| v0.4 | OPA + Job gates, CEL advanced, multi-hub federation |
| v1.0 | GA: multi-tenancy, RBAC, SLA |
→ Details: `docs/SPEC.md` §18
