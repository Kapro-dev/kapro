# Kapro Architecture

> **Kapro** is the canonical promotion layer for Kubernetes.
> It is the missing horizontal layer above Flux and ArgoCD —
> it owns *when* versions move between environments, not *how* they get deployed.

---

## The Core Insight

```
Kubernetes solved: "how do you run a container on a cluster?"
Kapro solves:      "when does a version move to the next environment?"
```

These are orthogonal problems. Kapro does not replace Flux or ArgoCD — it orchestrates them.

---

## Architecture Layers

```
┌──────────────────────────────────────────────────────────────┐
│                     KAPRO CORE                               │
│                                                              │
│  CRDs (api/v1alpha1)          7 resources                   │
│  ─────────────────────────────────────────────────────────  │
│  Release  Pipeline  Promotion  BatchRun                      │
│  Environment  EnvironmentGroup  Approval                     │
│                                                              │
│  State Machines (internal/controller)                        │
│  ─────────────────────────────────────────────────────────  │
│  Release:   Pending → Promoting → Progressing → Complete     │
│  Promotion: Pending → HealthCheck → Soaking →               │
│             MetricsCheck → WaitingApproval → Applying →     │
│             Converged → Failed                               │
│  Batch:     Pending → Resolving → Applying →                │
│             WaitingConvergence → GateCheck → Complete        │
│                                                              │
│  deps: controller-runtime + k8s.io/apimachinery only        │
└──────────┬───────────────────────────────────────────────────┘
           │ implements via KSI (Kapro Standard Interfaces)
           │
┌──────────▼─────────────────────────────────────────────────────────────┐
│                  KSI — KAPRO STANDARD INTERFACES (pkg/)                 │
│                                                                         │
│  KAI  pkg/actuator    Actuator     Apply / IsConverged / Rollback       │
│  KGI  pkg/gate        Gate         Evaluate(req) → Result               │
│  KHI  pkg/health      Assessor     AssessHealth(req) → Result           │
│  KVI  pkg/verification Verifier    Verify(req) → VerifyResult           │
│  KRI  pkg/oci         Service      Exists / Inspect / Tag / Copy        │
│  KNI  pkg/notification Notifier    Notify(event, policy)                │
│  KCI  pkg/provider    Connector    Connect / IsReachable                │
│                       RegistrationReader  GetRegistration               │
└──────────┬──────────────────────────────────────────────────────────────┘
           │
           │  Built-in                    External (gRPC, PluginRegistration CRD)
           ▼
┌──────────────────────────────────────────────────────────────────┐
│                  IMPLEMENTATIONS                                  │
│                                                                   │
│  KAI: internal/actuator/flux/      ← Flux OCIRepository patch    │
│       internal/actuator/argocd/    ← ArgoCD Application patch    │
│       internal/actuator/helm/      ← helm upgrade                │
│       internal/actuator/kserve/    ← KServe InferenceService     │
│                                                                   │
│  KGI: internal/gate/soak.go        ← bake period timer           │
│       internal/gate/metrics.go     ← Prometheus query            │
│       internal/gate/keda/          ← KEDA consumer lag           │
│       internal/gate/mlflow/        ← MLflow model metrics        │
│                                                                   │
│  KVI: internal/verification/cosign/ ← sigstore/cosign v2         │
│                                                                   │
│  KRI: internal/oci/oras/           ← oras.land/oras-go/v2        │
│                                                                   │
│  KNI: internal/notification/       ← Slack + Webhook (no deps)   │
│       internal/notification/engine/ ← argoproj/notifications-eng │
│                                                                   │
│  KCI: internal/provider/capi/      ← Cluster API                 │
│       internal/provider/ocm/       ← Open Cluster Management     │
│       internal/provider/crd/       ← ClusterRegistration CRDs    │
└──────────────────────────────────────────────────────────────────┘
```

---

## Analogy to Kubernetes

Kubernetes defined stable interfaces (CRI, CSI, CNI) so any runtime, storage, or network plugin could be swapped without touching the core. Kapro follows the same pattern:

| Kubernetes | Kapro |
|---|---|
| CRI — "how do you run a container?" | KAI — "how do you deploy a version?" |
| CSI — "how do you store data?" | KRI — "how do you fetch an artifact?" |
| CNI — "how do you network pods?" | KCI — "how do you connect to a cluster?" |
| _(no equivalent)_ | KGI — "how do you decide it's safe to promote?" |
| _(no equivalent)_ | KVI — "how do you verify the artifact is signed?" |
| _(no equivalent)_ | KNI — "how do you notify stakeholders?" |
| CRD + controller-runtime | CRD + controller-runtime (same library) |

**The interfaces ARE the product.** The implementations are optional.

---

## The Control Loop

Every state machine follows the Kubernetes reconcile pattern:

```
Watch CRD object
    ↓
Reconcile(ctx, req)
    ↓
1. Read current state  (get Promotion from API server)
2. Compare desired     (spec.phase vs status.phase)
3. Act to close gap    (call Actuator.Apply, Gate.Evaluate, etc.)
4. Update .status      (set conditions, phase, message)
5. Requeue             (return ctrl.Result{RequeueAfter: 30s})
```

State is stored in Kubernetes (etcd). Controllers are stateless and crash-safe.

---

## Plugin Extension via PluginRegistration CRD

External plugins implement one of the 7 KSI interfaces over gRPC and register themselves:

```yaml
apiVersion: kapro.io/v1alpha1
kind: PluginRegistration
metadata:
  name: my-pulumi-actuator
spec:
  type: Actuator            # KAI | KGI | KHI | KVI | KRI | KNI | KCI
  endpoint: grpc://pulumi-actuator-svc:9090
  # OR for sidecar:
  # socketPath: /var/run/kapro-plugins/pulumi-actuator.sock
```

The Kapro operator dials the endpoint and health-checks it on startup.
All KSI protos live in `proto/kapro/v1alpha1/`.

**Plugin selection order:**
1. Explicit `spec.actuator.type` on Environment → built-in match
2. PluginRegistration lookup by type + name
3. Error — no fallback to unexpected implementations

---

## Directory Layout

```
api/v1alpha1/         CRD type definitions (source of truth)
pkg/                  KSI public interface contracts (stable API surface)
  actuator/           KAI
  gate/               KGI
  health/             KHI
  verification/       KVI
  oci/                KRI
  notification/       KNI
  provider/           KCI
internal/
  controller/         State machine reconcilers
  actuator/           KAI implementations (flux, argocd, helm, kserve)
  gate/               KGI implementations (soak, metrics, keda, mlflow)
  health/             KHI implementations (gitops)
  verification/       KVI implementations (cosign)
  oci/                KRI implementations (oras)
  notification/       KNI implementations (dispatcher, engine)
  provider/           KCI implementations (capi, ocm, crd)
  mcp/                MCP server (AI assistant control plane)
proto/kapro/v1alpha1/ gRPC service definitions for all 7 KSIs
  gen/                Generated Go stubs (buf generate)
cmd/operator/         Main binary — wires all implementations
examples/             Demo YAMLs (ai-model-rollout, etc.)
docs/                 This file and other documentation
```

---

## Heavy Dependencies and Future Module Split

Three implementations carry heavy transitive dependency trees.
They are candidates for separate `go.mod` modules in a future phase:

| Package | Heavy dep | Future module |
|---|---|---|
| `internal/oci/oras/` | `oras.land/oras-go/v2` | `plugins/oci-oras/` |
| `internal/verification/cosign/` | `sigstore/cosign v2` | `plugins/verification-cosign/` |
| `internal/notification/engine/` | `argoproj/notifications-engine` | `plugins/notifications/` |

When split, the core binary (`cmd/operator-core/`) will compile without these deps,
giving integrators a ~20MB binary that still implements all 7 state machines.
Plugins are loaded via the PluginRegistration CRD at runtime.

---

## gRPC Transport (proto/kapro/v1alpha1/)

The gRPC protos make KSI language-agnostic — a Python or Rust team can implement
any interface without touching Go:

```
Kapro operator                  External plugin (any language)
─────────────────               ──────────────────────────────
PluginRegistration CRD
  → grpc.Dial(endpoint) ──────► implements ActuatorService
                                (proto/kapro/v1alpha1/actuator.proto)
```

Run `buf generate` to regenerate Go stubs after proto changes.
