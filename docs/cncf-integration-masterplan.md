# CNCF Integration Masterplan

Kapro is the fleet promotion layer for Kubernetes platforms. It decides
**where** and **when** an artifact version may move across a fleet, records
**why** that decision was made, and delegates **how** rollout happens to the
delivery systems teams already run.

Kapro should integrate with CNCF projects instead of competing with them:

- Flux, Argo CD, OCM, Sveltos, Helm, and platform controllers continue to own
  local apply, drift correction, health, and reconciliation.
- Argo Rollouts, Flagger, service mesh controllers, and ingress controllers
  continue to own traffic shifting and progressive rollout inside a cluster.
- Kapro owns fleet inventory, promotion waves, target binding, gates,
  approvals, convergence evidence, and PromotionRun audit history.

## Integration Matrix

| Project | Kapro relationship | Status |
|---|---|---|
| Flux | First-party backend. Kapro can update Flux-owned version inputs in push mode or record pull-mode desired state for a spoke to apply locally. | Built-in |
| Argo CD | First-party backend for `Application` and common brownfield patterns. ApplicationSet and app-of-apps adoption stay explicit through `PromotionSource.spec.units[]`. | Built-in |
| OCM ManifestWork | Kapro should select clusters and promotion waves, then delegate manifest placement to an OCM integration. The first implementation path should be a KAI actuator plugin or example, not a core rewrite. | Plugin/example target |
| Sveltos ClusterSummary | Kapro should decide when a cluster enters a wave; Sveltos should own feature/template application. The first path should be a KAI actuator plugin or example. | Plugin/example target |
| Helm | Helm charts are artifacts or local rollout mechanisms. Kapro can promote chart versions through OCI pull mode, Flux HelmRelease, Argo CD Helm sources, or a plugin; core should not own Helm lifecycle. | Via built-in backends or plugin |
| Kargo | Kargo can promote artifacts through environment stages upstream of Kapro. Kapro can consume the resulting artifact version and coordinate fleet waves. | Integration guidance |
| Argo Rollouts / Flagger | Kapro gates on rollout status and metrics. These tools own canary, blue-green, traffic split, and rollback mechanics inside the cluster. | Delegated runtime |
| Gateway API / Istio | Kapro may gate on status or metrics emitted by these systems. It does not mutate traffic policy directly unless a dedicated plugin owns that contract. | Delegated runtime |

## Built-In Versus Plugin Boundary

Built-in integrations are limited to delivery contracts Kapro already owns and
tests in the controller:

- `flux` backend driver;
- `argo` backend driver;
- `oci` pull desired-state driver for outbound-only spoke clusters.

Plugin-based integrations should use the KAI actuator contract when the
backend-specific write behavior is outside the core product contract. OCM
ManifestWork and Sveltos ClusterSummary are the best next examples because
they let Kapro stay at the fleet promotion layer while the integration owns the
backend-native apply object.

Future examples should include:

- `examples/plugins/ocm-manifestwork-actuator/` for writing or updating
  ManifestWork version inputs;
- `examples/plugins/sveltos-clustersummary-actuator/` for updating
  ClusterSummary or referenced values;
- conformance fixtures that prove idempotency, convergence, rollback behavior,
  and failure reporting for both plugins.

## Greenfield Architecture

For new fleets, prefer OCI pull mode when clusters need outbound-only
connectivity:

1. The hub records desired versions on `FleetCluster.spec.desiredVersions`.
2. The spoke controller authenticates outbound to the hub.
3. The spoke applies the local change through its configured backend.
4. The spoke reports `FleetCluster.status.currentVersions` and health.
5. Kapro advances waves only after gates, approvals, and convergence pass.

Use Flux or Argo CD as the backend when the platform already standardizes on
those controllers. Use plugins when the backend-specific write target is OCM,
Sveltos, or a proprietary platform controller.

## Brownfield Architecture

Brownfield adoption must be staged:

1. **Observe:** discover existing backend objects and report graph, health, and
   ownership evidence without writes.
2. **Adopt:** bind selected objects through `PromotionSource.spec.units[]` and
   explicitly grant Kapro permission to update version fields.
3. **Manage:** generate or own new backend objects only when the platform wants
   Kapro-managed sources.

Kapro must not copy backend credentials into its own CRDs. Argo CD, Flux, OCM,
Sveltos, and Helm repositories keep their existing Secrets and RBAC. Kapro
stores references, write contracts, and evidence.

## Production Rules

- Do not bypass the backend controller. Kapro writes intent; the backend
  reconciles rollout.
- Do not mutate arbitrary backend fields. Every write must be named in
  `PromotionSource.spec.units[]` or an integration-specific plugin contract.
- Do not expose hub APIs with the local development token model. Production
  deployments should use Kubernetes authn/authz or an identity-aware reverse
  proxy.
- Keep convergence evidence in Kubernetes status so operators can answer why a
  target advanced or stalled.
- Prefer plugin examples before adding new backend code to core.

## Implementation Backlog

1. Ship an OCM ManifestWork KAI example with conformance tests.
2. Ship a Sveltos ClusterSummary KAI example with conformance tests.
3. Add brownfield discovery examples for Argo CD ApplicationSet and Flux
   HelmRelease version fields.
4. Add integration conformance docs that separate write idempotency,
   convergence detection, rollback, and credential boundaries.
