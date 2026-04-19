# Kapro Roadmap

> **Ownership rule:** This file is the single source of truth for planned work.
> SPEC.md describes only what is implemented and shipped. Nothing moves into
> SPEC.md until the code is merged and the tests pass.
>
> When you pick up an item: move it to "In Progress".
> When the code lands: delete it from here and update SPEC.md to reflect the new reality.

---

## v0.3 — Extended Actuators & Cloud Providers

**Theme:** make Kapro production-ready for real multi-cloud fleets.

### KGI — Gate interface cleanup
- Remove `Result.Passed bool` entirely (deprecated in v0.2, kept for one cycle)
- Remove `NormalisePhase()` helper (no longer needed once Passed is gone)
- Update all tests that still set `Passed: true/false`

**Acceptance criteria:** `pkg/gate.Result` has no `Passed` field; `go build ./...` passes.

---

### KCI — Cloud connector implementations

The registry slot and ProviderSpec CRD fields exist for all clouds — only the `internal/provider/*/` implementation packages are missing.

GKE is **shipped** (see SPEC.md §9 and `internal/provider/gke/`). Remaining:

#### EKS — IRSA + STS (keyless)
- Package: `internal/provider/eks/`
- Auth: IAM Roles for Service Accounts → `github.com/aws/aws-sdk-go-v2`
- `AssumeRoleWithWebIdentity` using projected SA token at `/var/run/secrets`
- `Connect(ctx, env)` → calls `eks.DescribeCluster` for CA + endpoint, then signs requests with STS token

**Acceptance criteria:** `conformance/provider.RunSuite(t, connector)` passes against a live cluster; CI uses a mock.

---

### KAI — ArgoCD actuator
- Package: `internal/actuator/argocd/`
- `Apply` patches `Application.spec.source.targetRevision`
- `IsConverged` polls `Application.status.sync.status == Synced && health.status == Healthy`
- `Rollback` patches back to `previousVersion`
- Register as `"argocd"` in `cmd/operator/main.go`

**Acceptance criteria:** conformance suite passes; `Environment.spec.actuator.type: argocd` works end-to-end in integration test.

---

### Gate additions
- `JobGate` timeout enforcement (currently runs indefinitely if Job hangs)
- `WebhookGate` mTLS support (`spec.webhook.tlsSecretRef`)

---

## v0.4 — AI/ML Delivery, More Cloud Providers, gRPC Plugin Gateway

### KCI — Remaining cloud connectors
- **AKS** (`internal/provider/aks/`): Azure Managed Identity + AAD OIDC federation
- **DigitalOcean** (`internal/provider/digitalocean/`): API token from referenced Secret
- **StackIT** (`internal/provider/stackit/`): Service Account key from referenced Secret

### KAI/KGI — gRPC plugin gateway
This is the CRI equivalent for Kapro — enables out-of-process gate and actuator plugins.

Design prerequisites (must happen before implementation):
1. Define proto files: `spec/kai/v1alpha1/actuator.proto`, `spec/kgi/v1alpha1/gate.proto`
2. Generate Go from proto; replace hand-written Go interfaces with generated ones (or thin wrappers)
3. Implement `PluginGateway`: gRPC server the operator dials via Unix socket, registered via `PluginRegistration` CR
4. Ship ArgoCD actuator as first external plugin to validate the model

**Why v0.4 and not sooner:** gRPC gateway is a prerequisite for KSI (scheduler plugins) and for the external gate marketplace. It needs to be correct. Rushing it produces a broken extension model that's hard to fix without breaking vendors.

### KSI — Scheduler plugin framework
Design the `scheduler.Plugin` interface so pipeline ordering can be customised without modifying `ReleaseReconciler`. The gRPC plugin gateway (above) is a hard prerequisite.

### KAI — Additional actuators
- `KServe` — patches `InferenceService.spec.predictor.model.storageUri` for ML model delivery
- `Helm` — runs `helm upgrade --set image.tag=VERSION`
- `Sveltos` — ClusterSummary CR patch
- `OCM` — ManifestWork CR for Open Cluster Management

### KGI — Additional gates
- `KEDA` gate — ScaledObject queue lag threshold
- `MLflow` gate — Model registry metric threshold
- `KGateway` gate — traffic policy health and canary weight check
- `ArgoAnalysis` gate — `AnalysisRun`-based evaluation (first external plugin)
- `OPA` gate — policy-as-code

---

## v1.0 — GA

- Multi-tenancy: RBAC isolation per team
- Web dashboard
- CDEvents integration via webhook sinks
- SLA tracking and burn rate gates
- `ReleaseTrigger` CRD for autonomous triggers (MLflow model registered, OCI image pushed, Prometheus alert fired)
- `PluginGateway` + `PluginRegistration` CRDs (GA quality)

---

## Never-ship / Explicitly cut

These were considered and deliberately excluded. Do not re-open without an ADR.

| Item | Reason cut |
|------|-----------|
| In-memory gate state | Survives restarts only in etcd. Gate implementations must be stateless. |
| Mutable Releases | Audit trail must be append-only. Rollback = new Release. |
| Hub→spoke required network | Air-gapped environments need outbound-only. CRD path is non-negotiable default. |
| `ReleaseTrigger` in MVP | Autonomous triggers are post-MVP complexity. |
| `PluginGateway` in MVP | gRPC boundary needs proto design first; wrong to rush. |
