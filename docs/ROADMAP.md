# Kapro Roadmap

> **Ownership rule:** This file is the single source of truth for planned work.
> SPEC.md describes only what is implemented and shipped. Nothing moves into
> SPEC.md until the code is merged and the tests pass.
>
> When you pick up an item: move it to "In Progress".
> When the code lands: delete it from here and update SPEC.md to reflect the new reality.

---

## v0.3 — Extension Hardening

**Theme:** make the v1alpha1 extension contracts usable by real platform teams.

### KGI — Gate interface cleanup
- Remove `Result.Passed bool` entirely (deprecated in v0.2, kept for one cycle)
- Remove `NormalisePhase()` helper (no longer needed once Passed is gone)
- Update all tests that still set `Passed: true/false`

**Acceptance criteria:** `pkg/gate.Result` has no `Passed` field; `go build ./...` passes.

---

### KCI — Deferred / reconsidered

A generic cluster-provider abstraction (`pkg/provider`, `ProviderSpec` on
`MemberCluster`) was prototyped and then removed. Multi-cloud support is now a
question of (a) actuator implementations (`pkg/actuator`) and
(b) spoke-side bootstrap code in `internal/bootstrap`. There is no plan to
re-introduce a generic provider registry.

---

### KAI — External ArgoCD actuator plugin
- Package: `examples/plugins/argocd-actuator/`
- Implement the KAI gRPC contract.
- `Apply` patches `Application.spec.source.targetRevision`.
- `IsConverged` polls `Application.status.sync.status == Synced && health.status == Healthy`.
- `Rollback` patches back to `previousVersion`.
- Validate with `conformance/actuator`.

**Acceptance criteria:** conformance suite passes; example `PluginRegistration`
can load through the plugin gateway.

---

### Gate additions
- `JobGate` timeout enforcement (currently runs indefinitely if Job hangs)
- `WebhookGate` mTLS support (`spec.webhook.tlsSecretRef`)

---

## v0.4 — Plugin Runtime Maturity

### KCI — (superseded)
See ADR-006/ADR-007. No per-cloud connector packages planned.

### Plugin gateway dynamic reload
Completed in the brownfield production-readiness line. The operator watches
`PluginRegistration` readiness, registers or replaces runtime adapters after
successful probes, unloads stale or incompatible adapters, and preserves the
safety rule that only generation-fresh, ready registrations are used.

### KPI — Planner runtime dispatch
Completed in the brownfield production-readiness line. KPI planner plugins are
loaded into the release planner when the plugin gateway is enabled. They can
filter, defer, and score targets, while Kapro still owns binding and state.

**Acceptance criteria:** external planner plugins can filter/order/defer targets
without mutating `ReleaseTarget` state directly.

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
- `ReleaseTrigger` GA hardening: signed source verification, controller metrics, and operational docs.
- `PluginGateway` + `PluginRegistration` GA hardening: runtime soak, metrics, and compatibility policy.

---

## Never-ship / Explicitly cut

These were considered and deliberately excluded. Do not re-open without an ADR.

| Item | Reason cut |
|------|-----------|
| In-memory gate state | Survives restarts only in etcd. Gate implementations must be stateless. |
| Mutable Releases | Audit trail must be append-only. Rollback = new Release. |
| Hub→spoke required network | Air-gapped environments need outbound-only. CRD path is non-negotiable default. |
