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

### KCI — Deferred / reconsidered

A generic cluster-provider abstraction (`pkg/provider`, `ProviderSpec` on
`MemberCluster`) was prototyped and then removed — see
`docs/adr/ADR-006-multi-cloud-provider-onboarding.md` and
`docs/adr/ADR-007-kxi-interface-family.md`. Multi-cloud support is now a
question of (a) actuator implementations (`pkg/actuator`) and
(b) spoke-side bootstrap code in `internal/bootstrap`. There is no plan to
re-introduce a generic provider registry.

---

### KAI — ArgoCD actuator
- Package: `internal/actuator/argocd/`
- `Apply` patches `Application.spec.source.targetRevision`
- `IsConverged` polls `Application.status.sync.status == Synced && health.status == Healthy`
- `Rollback` patches back to `previousVersion`
- Register as `"argocd"` in `cmd/operator/main.go`

**Acceptance criteria:** conformance suite passes; `MemberCluster.spec.actuator.type: argocd` works end-to-end in integration test.

---

### Gate additions
- `JobGate` timeout enforcement (currently runs indefinitely if Job hangs)
- `WebhookGate` mTLS support (`spec.webhook.tlsSecretRef`)

---

## v0.4 — AI/ML Delivery, More Cloud Providers, gRPC Plugin Gateway

### KCI — (superseded)
See ADR-006/ADR-007. No per-cloud connector packages planned.

### KAI/KGI — gRPC plugin gateway
This is the CRI equivalent for Kapro — enables out-of-process gate and actuator plugins.

Design prerequisites (must happen before implementation):
1. Generate Go from `spec/kai/v1alpha1/actuator.proto` and `spec/kgi/v1alpha1/gate.proto`
2. Replace hand-written Go interfaces with generated ones (or thin wrappers)
3. Implement `PluginGateway`: gRPC boundary for endpoints registered via `PluginRegistration`
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
- Authenticated approval attribution:
  front `/approve` and `/reject` with an SSO-aware reverse proxy or ingress
  (for example oauth2-proxy, Pomerium, IAP, ALB auth), trust only the
  injected identity header from that proxy, and record the authenticated
  human identity instead of relying on token-packed `ApprovedBy`. This is
  the simple correct path for real per-click audit attribution when approval
  links may be shared across multiple approvers.
- CDEvents integration via webhook sinks
- SLA tracking and burn rate gates
- `ReleaseTrigger` CRD for autonomous triggers (OCI image pushed first; MLflow model registered and Prometheus alert fired later). Must follow `docs/ADR-002-release-trigger.md`.
- `PluginGateway` + `PluginRegistration` CRDs (GA quality)

---

## Never-ship / Explicitly cut

These were considered and deliberately excluded. Do not re-open without an ADR.

| Item | Reason cut |
|------|-----------|
| In-memory gate state | Survives restarts only in etcd. Gate implementations must be stateless. |
| Mutable Releases | Audit trail must be append-only. Rollback = new Release. |
| Hub→spoke required network | Air-gapped environments need outbound-only. CRD path is non-negotiable default. |
| `ReleaseTrigger` in MVP | Autonomous triggers are post-MVP complexity. When implemented, follow ADR-002 and make it safe by default. |
| `PluginGateway` in MVP | gRPC boundary needs proto design first; wrong to rush. |
