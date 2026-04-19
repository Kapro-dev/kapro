# Project: Kapro

**Full name:** Kapro — Kubernetes Progressive Delivery Orchestrator
**Status:** Active — MVP (v0.1) in development
**Canonical spec:** `docs/SPEC.md` (always update this as the source of truth)

---

## What It Is

Kapro is a Kubernetes-native control plane that sits **above** GitOps tools (Flux, ArgoCD) and
governs *when* and *in what order* cluster environments receive a new version of a workload.

It models progressive delivery as a two-level DAG:
1. **Pipeline DAG** — `Release.spec.pipelines[].dependsOn`
2. **Stage DAG** — `Pipeline.spec.stages[].dependsOn`

Each delivery attempt is a `Sync` object, which drives a finite state machine.

---

## Core Mental Model

| Kubernetes Concept | Kapro Equivalent |
|--------------------|-----------------|
| Pod | Environment |
| Node | ManagedCluster |
| Container run | Sync |
| Init container | Verification gate |
| Readiness probe | HealthCheck gate |
| Resource limits | GatePolicy |
| Scheduler | Pipeline controller |

---

## MVP Scope (v0.1)

**In scope:**
- Single hub cluster
- FluxActuator only
- 4 built-in gates: Soak, Metrics, Approval, Verification (cosign)
- CRD provider (kapro-cluster-controller heartbeat)
- CEL gate (`type: cel`)
- Kubernetes Job gate (`type: job`)
- Webhook gate (`type: webhook`)
- One-click HMAC approval/reject webhook
- Prometheus metrics: `kapro_sync_transitions_total`, `kapro_gate_evaluations_total`, `kapro_stage_duration_seconds`
- 11 CRDs

**Explicitly cut from MVP:**
- ReleaseTrigger, PluginGateway, PluginRegistration
- KServe, ArgoCD, Sveltos, OCM actuators
- CAPI, OCM, OpenShift, Rancher providers
- Argo Analysis gate, plugin-gateway gate, KEDA gate, MLflow gate
- Multi-hub federation

---

## Sync FSM

```
Pending → Verification → HealthCheck → Soaking → MetricsCheck → WaitingApproval → Applying → Converged
                                                                                              ↘ Failed
```

Each phase transition emits a Kubernetes event and increments `kapro_sync_transitions_total`.

---

## Key Interfaces

### Gate
```go
type Gate interface {
    Evaluate(ctx context.Context, req Request) (Result, error)
}
// All gate state in Sync.Status.Gates[] — gates are stateless
// CEL variables: args.*, environment.*, sync.*  (NOT promotion.*)
```

### Actuator
```go
type Actuator interface {
    Apply(ctx context.Context, req ApplyRequest) error
    // v0.2: appKey added explicitly — key in ManagedCluster.status.currentVersions
    IsConverged(ctx context.Context, env *v1alpha1.Environment, version, appKey string) (bool, error)
    Rollback(ctx context.Context, env *v1alpha1.Environment, previousVersion string) error
}
```

### GateSet (wired in one place — all 5 gates including CEL)
```go
func BuildGateSet(c client.Client) GateSet {
    return GateSet{
        Soak:         &internalgate.SoakGate{},
        Metrics:      &internalgate.MetricsGate{},
        Approval:     &internalgate.ApprovalGate{Client: c},
        Verification: &internalgate.VerificationGate{...},
        CEL:          &celgate.Gate{Client: c},  // v0.2: moved here from inline construction
    }
}
```

---

## Architecture Rules (invariants — never violate)

1. `SyncReconciler` fields use `gate.Gate` interface, never concrete types
2. `BuildGateSet()` returns a **complete** GateSet — never half-wired
3. Gate state lives **only** in `Sync.Status.Gates[]` — gates are stateless
4. Rollback = new Release pointing at old OCI digest — never mutate a Release
5. Metrics subsystem: `"sync"` (not `"promotion"` — old name)
6. CEL variable is `"sync"` not `"promotion"` in both env declaration and activation map
7. deepcopy in `zz_generated.deepcopy.go` is **manually maintained** (no Go codegen in sandbox)

---

## CRD Inventory

### User-Facing (7)
| CRD | Purpose |
|-----|---------|
| Artifact | OCI image reference + digest |
| Environment | Target: cluster + namespace + actuator config |
| Pipeline | Ordered DAG of stages |
| Release | Immutable: "deliver artifact X through pipeline Y" |
| GatePolicy | Per-environment gate enablement + args |
| GateTemplate | Reusable parameterised gate definition |
| ManagedCluster | Fleet node registration |

### System-Generated (4)
| CRD | Purpose |
|-----|---------|
| Sync | One delivery attempt FSM instance |
| Approval | User unblocks WaitingApproval by creating this |
| ReleaseReport | Audit record: all Syncs for a Release |
| BootstrapToken | Short-lived HMAC for first cluster join |

---

## Key Files
| File | Role |
|------|------|
| `docs/SPEC.md` | **Living spec — canonical reference** |
| `api/v1alpha1/types.go` | All CRD structs |
| `api/v1alpha1/zz_generated.deepcopy.go` | Manual deepcopy |
| `internal/controller/sync_controller.go` | FSM reconciler |
| `pkg/controllermanager/controllers.go` | Gate wiring + controller init |
| `pkg/gate/gate.go` | Gate interface |
| `pkg/actuator/actuator.go` | Actuator interface |
| `cmd/operator/main.go` | Binary: registers FluxActuator only |

---

## Roadmap
| Version | Focus |
|---------|-------|
| v0.1 | MVP: single-hub, Flux, 4 gates, 11 CRDs |
| v0.2 | Multi-hub federation |
| v0.3 | ArgoCD actuator + Webhook gate |
| v0.4 | OPA + Job gates, CEL advanced |
| v1.0 | GA: multi-tenancy, RBAC, SLA guarantees |
