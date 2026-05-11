# Changelog

## v0.3.0 — Spoke-Local Flux + CLI + GCP-Native

Production-ready spoke-local delivery mode with complete CLI and GCP integration.
All operations use Go SDK — no gcloud, helm, flux, or kubectl dependencies.

### Spoke-Local Flux (deliveryMode: spoke)

- **OCI bundle pull model** — spoke's own Flux controllers pull and reconcile bundles from GAR
- **Per-wave directories** — bundle structured as wave-00/, wave-01/, wave-02/ to avoid Kustomization resource conflicts
- **Wave Kustomization DAG** — each wave has dependsOn to previous wave, visible in k9s on every spoke
- **HelmReleases without kubeConfig** — spoke's helm-controller reconciles locally (not hub pushing remotely)
- **Spoke actuator** — patches OCIRepository tag on spoke, reads Flux status directly for convergence
- **Both modes coexist** — `deliveryMode: push` (v0.2 behavior) and `deliveryMode: spoke` (new) on same hub

### CLI (`kapro`)

- **`kapro hub init`** — bootstraps hub cluster: flux-operator, FluxInstance, CRDs, Fleet registration. Interactive project/cluster selection when no flags provided.
- **`kapro hub registry list/create/add`** — centralized GAR registry management. Saved to `~/.kapro/config.yaml`.
- **`kapro spoke add`** — adds spoke cluster: auto-installs flux-operator + FluxInstance + Fleet registration + IAM bindings. Single command, zero manual steps.
- **`kapro fleet list/sync`** — Fleet membership management. `sync` auto-discovers and registers all Fleet clusters.
- **`kapro bundle generate --push`** — reads KaproApp from hub, generates per-wave bundle, validates, pushes via ORAS Go SDK. Used by CI pipelines.
- **`kapro status`** — live fleet dashboard with colored phases (Converged/Converging/Failed), per-cluster version, health, heartbeat.
- **`~/.kapro/config.yaml`** — persistent CLI context. `hub init` writes it, all commands read project/registry from it.
- **Spinner UX** — braille animation with colored status symbols: ✔ (success), ✗ (failure), ⚠ (warning), ℹ (info).

### GCP-Native (zero shell dependencies)

- **ORAS Go SDK** for OCI bundle push (replaces `flux push artifact` shell exec)
- **Fleet Hub API** for cluster discovery and membership registration
- **Container API** for cluster endpoint resolution and location auto-detection
- **IAM + CRM APIs** for cross-project spoke access and service account bindings
- **Artifact Registry API** for GAR repository creation and listing
- **Workload Identity** for authentication — zero credentials on GKE, gcloud fallback for local dev

### Production Scale (150 clusters)

- **Parallel spoke bootstrap** — bounded concurrency (10 at a time) via semaphore channel
- **Spoke client cache** — sync.Map with 5min TTL, health probe before reuse (expired tokens auto-invalidate)
- **Version-change detection** — skip bootstrap if MemberCluster already converged at target version
- **Error isolation** — failing spoke doesn't block other spokes in the reconcile loop
- **Embedded CRDs** — FluxInstance, ResourceSet, and 8 Kapro CRDs via go:embed (no external file deps)

### KaproApp Validation

- No empty component names, versions, or registries
- No duplicate component or registry names
- DependsOn references must exist
- Dependencies must be in same or earlier wave
- Registry URLs must match declared type (oci:// for OCI type)

### Release Flow (e2e verified on GKE)

- Release CR → Progressing → ReleaseTarget created per cluster
- FSM: Verification ✔ → HealthCheck ✔ → Applying
- SpokeFluxActuator.Apply() → patches OCIRepository tag on spoke
- Spoke pulls new bundle → wave DAG reconverges
- MemberCluster.status.version updated from OCIRepository tag

### Breaking Changes

- `kapro cluster bootstrap` removed — use `kapro spoke add`
- `kapro gcp *` commands removed — use `kapro fleet list/sync`
- `kapro world` / `kapro fleet` removed — use `kapro status`
- `kapro promote` removed — use Release CR
- CLI commands renamed: `cluster add` → `spoke add`

---

## v0.2.0 — Push Model Complete

Flux Operator actuator, Fleet API integration, and GCP SDK.

### Flux Operator Actuator

- **ResourceSet-based delivery** — patches ResourceSet inputs on hub, Flux Operator renders per-cluster HelmReleases with kubeConfig
- **KaproApp component spec v2** — registries, waves, dependsOn, values, valuesFrom, timeout, retries, CRDs, suspend
- **ResourceSet generator** — builds HelmRepositories + HelmReleases from KaproApp spec
- **Convergence check** — scans ResourceSet inventory for HelmRelease Ready status

### Fleet API + GCP SDK

- **GCPFleetProvider** — auto-discovers clusters from Fleet memberships via Go SDK
- **GCPBasicProvider** — GKE DNS endpoint + Workload Identity, zero gcloud dependency
- **Auto-generate kubeconfig secrets** from Fleet API + WI tokens
- **Token caching** — shared OAuth2 token source, auto-refresh

### Fleet Observability

- **MemberCluster status sync** — reads HelmRelease status, writes version + health to MemberCluster
- **k9s columns** — version, phase, convergence, healthy visible in `kubectl get memberclusters`

---

## v0.1.0 — Initial Release

First release of Kapro: CRDs, FSM, gates, approval webhook.

### CRDs (7)

- Artifact, Pipeline, Release, ReleaseTarget, MemberCluster, Approval, Source

### Controllers (5)

- ReleaseReconciler (two-level DAG), ReleaseTargetReconciler (10-state FSM), SourceReconciler, ApprovalReconciler, CSRApprovalReconciler

### Gates (8)

- Soak, Approval, Metrics, HealthCheck, CEL, Job, Webhook, Verification

### CLI

- cluster bootstrap/join, get releases/targets, approve/reject, rollback, release create, world

### Scalability

- Controller sharding, lease-based heartbeat, field-indexed lookups, conditional poll
