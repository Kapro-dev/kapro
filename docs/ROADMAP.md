# Kapro Roadmap

This file tracks planned work only. Implemented work belongs in
[`SPEC.md`](SPEC.md) and release history belongs in [`CHANGELOG.md`](../CHANGELOG.md).

## v0.5 - Operational Hardening

- Add automated CRD schema drift checks to CI.
- Add markdown link checking to CI for README and docs.
- Add release-note/changelog validation for required sections.
- Publish benchmark evidence for repository size, backend object count, target
  count, and PromotionRun fanout.
- Add dashboard-ready metrics for inline gate decisions, backend refresh,
  plugin probe failures, and target duration percentiles.
- Expand install verification to cover upgrade from the previous tagged alpha.

## v0.6 - Backend and Plugin Depth

- Add first-party examples for additional KAI actuators such as Helm, KServe,
  Open Cluster Management ManifestWork, and Sveltos ClusterSummary.
- Add KGI gate examples for OPA, Argo AnalysisRun, queue-lag checks, and
  service-level objective burn-rate checks.
- Publish a compatibility matrix for external KAI, KGI, and KPI plugins.
- Decide whether notification routing needs a separate public API or should
  remain inline on gate/stage policy.

## v1.0 - GA

- Publish a stable Kubernetes API version with conversion and migration
  guidance from `kapro.io/v1alpha1`.
- Validate at least one tagged upgrade path and document downgrade limits.
- Publish broad Argo and Flux E2E evidence for a tagged release.
- Complete independent review of security boundaries, RBAC, hub gateway
  exposure, plugin trust, and Secret handling.
- Publish real-world soak evidence from multiple non-maintainer operators.
- Harden authenticated approval attribution through an SSO-aware reverse proxy
  or ingress that records the authenticated human identity.

## Explicitly Excluded

These ideas were considered and cut. Reopen only with a new ADR.

| Item | Reason cut |
|---|---|
| In-memory gate state | Gate progress must survive controller restarts through Kubernetes status. |
| Mutable PromotionRuns | Audit trail must be append-only. Rollback is a new PromotionRun. |
| Required hub-to-spoke network path | Air-gapped fleets need outbound-only options. |
| Generic cluster-provider registry | FleetCluster inventory plus concrete bootstrap paths are simpler and safer. |
