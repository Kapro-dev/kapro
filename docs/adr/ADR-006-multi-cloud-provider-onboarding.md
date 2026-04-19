# ADR-006: Multi-Cloud Provider Onboarding

**Status:** Proposed  
**Date:** 2026-04-19  
**Deciders:** Kapro maintainers  
**Supersedes:** N/A  
**Related:** ADR-002 (Why CRD Provider), ADR-005 (Why BuildGateSet takes client.Client)

---

## Context

Kapro v0.1 ships with a single cluster connectivity backend: the **CRD provider** (`internal/provider/crd`). The kapro-cluster-controller runs on each spoke cluster, writes ManagedCluster heartbeats to the hub, and polls `spec.desiredVersion` — no direct network path from hub to spoke is required.

This works on every cloud, but it requires deploying the cluster-controller agent on every spoke. For clouds with managed Kubernetes services (GKE, EKS, AKS, DigitalOcean, StackIT), a **direct-connect path** is possible: the hub authenticates to the cloud API using cloud IAM and fetches a kubeconfig on demand. No agent needed on the spoke.

We need to decide:
1. When to build direct-connect connectors and in what order
2. How the hub dispatches to the right connector at runtime
3. What cloud-specific config lives in the CRD vs in Secrets
4. Whether Path A (outbound) and Path B (direct) can coexist per-environment

---

## Decision

### Two-path model — both supported, coexist per environment

```
Path A: Outbound (CRD Provider)          Path B: Direct Connect (KCI Connector)
──────────────────────────────           ──────────────────────────────────────
Spoke runs cluster-controller            Hub calls cloud API with IAM credential
No hub→spoke network needed              Requires hub→spoke API server access
Works air-gapped                         No agent needed on spoke
Bootstrap via BootstrapToken HMAC        Bootstrap via cloud IAM binding
EnvironmentSpec.provider.type: ""        EnvironmentSpec.provider.type: gke|eks|...
Status quo (already shipped)             v0.3+ per-cloud
```

**Default:** `spec.provider.type: ""` resolves to CRD provider (backward compatible).

**Runtime dispatch:** A new `provider.Registry` (mirrors `actuator.Registry`) resolves `env.Spec.Provider.Type → Connector`. When type is `""` or `"crd"`, the `RegistrationReader` path is used; for named providers, the `Connector` path is used.

### Delivery order

| Version | Providers |
|---------|-----------|
| v0.1 (now) | CRD provider only |
| v0.3 | GKE (Workload Identity + Connect Gateway) |
| v0.3 | EKS (IRSA + STS) |
| v0.4 | AKS (Managed Identity + AAD OIDC) |
| v0.4 | DigitalOcean (API token in Secret) |
| v0.4 | StackIT (Service Account key in Secret) |

GKE and EKS first because they have the richest keyless IAM story (Workload Identity / IRSA) — no static credentials stored in Secrets.

### Config split: CRD vs Secret

| Config type | Where |
|-------------|-------|
| Cloud endpoint, cluster name, region, project/account ID | `EnvironmentSpec.provider.*` (CRD) |
| API tokens, service account keys, client secrets | `Secret` in `kapro-system`; referenced by name |
| Workload Identity / IRSA (keyless) | Annotation on hub K8s ServiceAccount — no Secret at all |

**Never store credentials in CRD fields.** Always reference a Secret by name.

---

## Options Considered

### Option A: CRD provider only (status quo)

Keep the outbound-only model indefinitely. All clouds deploy cluster-controller.

| Dimension | Assessment |
|-----------|------------|
| Complexity | Low — already implemented |
| Auth security | High — HMAC bootstrap + SA token rotation |
| Agent overhead | Medium — must deploy + operate cluster-controller per spoke |
| Cloud-native IAM | No — cannot use Workload Identity |
| Air-gap support | Full |
| Time to first cluster | ~5 min (Helm install) |

**Pros:** No new code. Works everywhere including on-prem and air-gapped.  
**Cons:** Requires agent management on every cluster. Cannot leverage keyless cloud IAM.

### Option B: Direct-connect only (replace CRD provider)

Replace CRD provider with cloud-native connectors everywhere.

| Dimension | Assessment |
|-----------|------------|
| Complexity | High — per-cloud SDK integrations |
| Auth security | High (keyless IAM) |
| Agent overhead | None |
| Cloud-native IAM | Full |
| Air-gap support | None |
| On-prem support | None without custom connector |

**Pros:** No cluster-controller to manage. Keyless auth.  
**Cons:** Breaks air-gap and on-prem. Hub must have network access to all spoke API servers. One codebase change per cloud SDK version bump.

### Option C (Chosen): Both paths, runtime-selectable

Per-Environment choice: `spec.provider.type` selects the backend. Existing environments default to CRD provider.

| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium — need `provider.Registry`, per-cloud Connector impl |
| Auth security | High — keyless for cloud-native, HMAC for outbound |
| Agent overhead | Optional — only needed if using Path A |
| Cloud-native IAM | Full (Path B) |
| Air-gap support | Full (Path A) |
| On-prem support | Full (Path A) |

**Pros:** Best of both. Operators choose based on their constraints.  
**Cons:** Two code paths to maintain. Registry adds dispatch indirection.

---

## Trade-off Analysis

The CRD provider's outbound pattern is a strategic differentiator for air-gapped and on-prem fleets — the kind of environments where GitOps with Flux shines. We should not deprecate it.

The direct-connect pattern is preferred for cloud-native teams who want zero agent overhead and keyless IAM. Workload Identity / IRSA are production-grade auth mechanisms that are already widely understood.

The `provider.Registry` pattern adds minimal complexity (50 lines, same as `actuator.Registry`) and keeps the `SyncReconciler` unchanged — it calls `provider.Resolve(env.Spec.Provider.Type)` the same way it calls `actuator.Resolve(env.Spec.Actuator.Type)`.

---

## Implementation Plan

### Step 1: `pkg/provider/registry.go` (G1 fix — must precede all cloud work)

```go
package provider

import (
    "fmt"
    "sync"
)

type Registry struct {
    mu    sync.RWMutex
    impls map[string]Connector
}

func NewRegistry() *Registry {
    return &Registry{impls: make(map[string]Connector)}
}

func (r *Registry) Register(typeName string, c Connector) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, exists := r.impls[typeName]; exists {
        return fmt.Errorf("provider type %q already registered", typeName)
    }
    r.impls[typeName] = c
    return nil
}

func (r *Registry) Resolve(typeName string) (Connector, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    if typeName == "" || typeName == "crd" {
        return nil, nil // caller uses RegistrationReader path
    }
    c, ok := r.impls[typeName]
    if !ok {
        return nil, fmt.Errorf("no provider registered for type %q — available: %v", typeName, r.keys())
    }
    return c, nil
}
```

Wire into `ControllerContext`:
```go
// ControllerContext addition
ProviderRegistry *provider.Registry
```

### Step 2: Extend `types.go` with cloud ProviderSpec fields

Add `GKEProviderSpec`, `EKSProviderSpec`, `AKSProviderSpec`, `DigitalOceanProviderSpec`, `StackITProviderSpec` as detailed in ARCHITECTURE_REVIEW.md §3.2.

Update deepcopy for new pointer fields.

### Step 3: Per-cloud Connector implementations (v0.3+)

Each cloud gets its own package under `internal/provider/{gke,eks,aks,digitalocean,stackit}/`:

```
internal/provider/
├── crd/           ← already shipped (RegistrationReader)
├── gke/           ← v0.3: Workload Identity + Connect Gateway
├── eks/           ← v0.3: IRSA + STS
├── aks/           ← v0.4: Managed Identity + AAD OIDC
├── digitalocean/  ← v0.4: API token in Secret
└── stackit/       ← v0.4: Service Account key in Secret
```

Each package:
1. Implements `kci.Connector`
2. Has `var _ kci.Connector = (*Connector)(nil)` compile check
3. Passes `conformance/provider.RunSuite(t, &Connector{})`
4. Reads credentials from K8s Secrets only (never from CRD fields)
5. Uses keyless IAM when the cloud supports it

### Step 4: Registration in `cmd/operator/main.go`

```go
// GKE connector — registered when running on GCP with Workload Identity
if gkeEnabled {
    if err := providerRegistry.Register("gke", &gkeprovider.Connector{
        Client: mgr.GetClient(),
    }); err != nil {
        // ...
    }
}
```

Connector binaries compiled with build tags or enabled via env var — no recompilation for disabling.

---

## Consequences

**What becomes easier:**
- Onboarding a new GKE/EKS/AKS cluster: apply one Environment YAML, no Helm install needed (Path B)
- Air-gapped clusters: unaffected, CRD provider still works (Path A)
- Audit: `ClusterCapabilities.cloud` + `accountID` make every delivery traceable to a cloud account
- Multi-cloud pipeline waves: `spec.topology.cloud` field on Environment enables cloud-aware stage selectors

**What becomes harder:**
- Testing: need to mock cloud SDK calls in unit tests (use `Connector` interface for testability)
- Credential rotation: DigitalOcean and StackIT tokens in Secrets need a rotation story
- Network policy: hub pod needs egress to cloud API endpoints when using Path B

**What we'll need to revisit:**
- If STACKIT adds Workload Identity (planned for 2026), `StackITProviderSpec` can drop `serviceAccountKeySecretRef`
- If DigitalOcean adds OIDC federation, same simplification
- `BootstrapToken` TTL and rotation policy for long-running cluster-controller installations (Path A)

---

## Appendix: Auth Matrix

| Cloud | Path A auth | Path B auth (v0.3+) | Static secret? |
|-------|-------------|---------------------|----------------|
| GKE | HMAC bootstrap → SA token | Workload Identity | No |
| EKS | HMAC bootstrap → SA token | IRSA (STS) | No |
| AKS | HMAC bootstrap → SA token | Managed Identity + AAD OIDC | No |
| DigitalOcean | HMAC bootstrap → SA token | API token in Secret | Yes (v0.4 gap) |
| StackIT | HMAC bootstrap → SA token | SA key JSON in Secret | Yes (v0.4 gap) |
| On-prem / air-gap | HMAC bootstrap → SA token | N/A | No |
