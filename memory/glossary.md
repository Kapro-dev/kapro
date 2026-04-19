# Kapro Glossary

Full decoder ring for all Kapro terms, acronyms, and internal language.

---

## Core Concepts

| Term | Meaning | Notes |
|------|---------|-------|
| Sync | One delivery attempt of a version to one environment | The FSM unit; replaces old name "Promotion" |
| Release | Immutable record: "deliver artifact X through pipeline Y" | Never mutate; rollback = new Release |
| Pipeline | Ordered DAG of stages, each targeting an Environment | Top-level delivery plan |
| Stage | One node in a Pipeline; wraps an Environment | Has `dependsOn` for DAG ordering |
| Environment | Logical delivery target: cluster + namespace + actuator | Like a Kubernetes Pod |
| Artifact | OCI image reference + digest | Immutable pointer to what's being delivered |
| GatePolicy | Per-environment config: which gates are enabled + args | Governs if a Sync can advance |
| GateTemplate | Reusable parameterised gate (`cel\|job\|webhook`) | DRY gate definitions |
| ManagedCluster | Hub-side CRD; written by spoke cluster-controller heartbeat | Like a Kubernetes Node |
| BootstrapToken | Short-lived HMAC token for first cluster registration | Expires after use |
| ReleaseReport | System-generated audit record aggregating all Syncs per Release | Append-only |
| Approval | User-created CRD to unblock `WaitingApproval` phase | Created via UI or one-click URL |
| Gate | Stateless evaluator → `Result{Passed,Inconclusive,Failed}` | All state in Sync.Status.Gates[] |
| Actuator | Applies version to cluster; checks convergence; rolls back | MVP: FluxActuator only |
| GateSet | Struct holding all 4 wired gate instances | Built by `BuildGateSet(client.Client)` |
| CRD provider | Fleet topology via kapro-cluster-controller heartbeat | No direct network path required |
| Hub | Cluster running the Kapro operator | Central control plane |
| Spoke | Managed cluster running kapro-cluster-controller | Writes ManagedCluster to hub |

---

## Sync FSM Phases

| Phase | Meaning |
|-------|---------|
| Pending | Waiting for upstream pipeline dependencies |
| Verification | Cosign image signature check |
| HealthCheck | Current environment health (Prometheus/k8s) |
| Soaking | Time-based wait before proceeding |
| MetricsCheck | Prometheus metric threshold evaluation |
| WaitingApproval | Human approval required |
| Applying | Actuator is applying the version |
| Converged | Successfully delivered |
| Failed | Terminal failure (check `failurePolicy`) |

---

## Gate Types (MVP)

| Type | Implementation | Key config |
|------|---------------|------------|
| `cel` | CEL expression evaluated against context | `expression: "sync.version != ''"` |
| `job` | Kubernetes Job runs to completion | `image`, `command`, `env` |
| `webhook` | HTTP POST to external URL | `url`, `caBundle`, `timeout` |
| `soak` | Built-in: time-based hold | `duration: "30m"` |
| `metrics` | Built-in: Prometheus query threshold | `query`, `threshold` |
| `approval` | Built-in: waits for Approval CRD | `notifyChannels` |
| `verification` | Built-in: cosign signature check | `keyRef` or `keyless` |

**Cut from MVP (do not use):** `argo-analysis`, `plugin-gateway`, `keda`, `mlflow`, `shadow`, `kgateway`

---

## CEL Context Variables

| Variable | Type | Meaning |
|----------|------|---------|
| `args.*` | map[string]string | Gate template args (overridable per policy) |
| `environment.*` | Environment spec | Target environment details |
| `sync.*` | Sync status | Current delivery attempt state |

⚠️ Use `sync.*` NOT `promotion.*` — old name was renamed

---

## Actuator Types (MVP)

| Type | Status |
|------|--------|
| `flux` | ✅ MVP |
| `argocd` | 🔮 v0.3 |
| `helm` | 🔮 future |
| `kserve` | ❌ cut |
| `sveltos` | ❌ cut |
| `ocm` | ❌ cut |

---

## Provider Types (MVP)

| Type | Status |
|------|--------|
| CRD provider | ✅ MVP (only one) |
| CAPI | ❌ cut |
| OCM | ❌ cut |
| OpenShift | ❌ cut |
| Rancher | ❌ cut |

---

## Code Conventions

| Convention | Detail |
|-----------|--------|
| Gate fields in SyncReconciler | Always `gate.Gate` interface, never concrete type |
| BuildGateSet | Takes `client.Client`, returns complete GateSet |
| Metrics subsystem | `"sync"` (not `"promotion"`) |
| CEL variable name | `"sync"` in both env declaration AND activation map |
| deepcopy | Manually maintained — no Go codegen in sandbox |
| Rollback | New Release pointing at old OCI digest |

---

## Prometheus Metrics

| Metric | Labels | Meaning |
|--------|--------|---------|
| `kapro_sync_transitions_total` | `phase`, `result` | FSM phase transitions |
| `kapro_gate_evaluations_total` | `gate`, `result` | Gate evaluation outcomes |
| `kapro_stage_duration_seconds` | `pipeline`, `stage` | Stage completion time |

---

## Controller Manager Pattern

Modelled after `k8s.io/cloud-provider` CCM:
- `Register("name", initFn)` pattern
- `KAPRO_CONTROLLERS=*,-releasereport` for selective enabling
- `startSyncController` wires gates directly from GateSet (no adapters)

---

## Key ADRs (from docs/SPEC.md Appendix A)

| Decision | Why |
|----------|-----|
| `Sync` not `Promotion` | Avoids confusion with "code promotion" in CI/CD |
| CRD provider for fleet | No direct network path; works air-gapped |
| Immutable Releases | Auditability + safe rollback semantics |
| `gate.Gate` is stateless | State in etcd, not in-process; survives restarts |
| `BuildGateSet` takes `client.Client` | Constructor completeness; no half-wired structs |
