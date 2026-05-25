# Kapro

Kapro is a promotion control plane for Kubernetes fleets. It coordinates when
an artifact version should move across clusters while Flux, Argo CD, OCI pull
agents, and other delivery systems keep owning local rollout mechanics.

Kapro answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

## Product Boundaries

Kapro owns promotion policy, ordering, approvals, decision traces, and delivery
evidence. It does not own artifact builds, cluster lifecycle, secret storage,
or the local reconciler that actually rolls out workloads.

Permanent non-goals: Kapro is not a Helm registry, CI runner, manifest store,
cluster provisioner, or secret store.

## Start Here

- [Concepts](concepts/concepts.md) explains the API objects and lifecycle.
- [Install](getting-started/install.md) shows the supported operator install paths.
- [Adoption Guide](getting-started/adoption.md) helps choose greenfield,
  existing Argo CD, existing Flux, or OCI pull mode.
- [Adoption CLI](getting-started/adoption-cli.md) shows the guided `kapro create`,
  `kapro sample`, `kapro doctor`, and `kapro explain` paths.
- [First Promotion in 10 Minutes](getting-started/first-promotion-10min.md) is the shortest
  working path.
- [Direct Apply Quickstart](getting-started/quickstart-direct.md),
  [Flux Quickstart](getting-started/quickstart-flux.md), and
  [Argo CD Quickstart](getting-started/quickstart-argo.md), and
  [OCI Quickstart](getting-started/quickstart-oci.md) cover the 0.6 public
  preview profiles.
- [Substrates](concepts/substrates.md) explains Flux, Argo CD, OCI, and plugin delivery
  options.
- [Operations](operations/operations.md) covers day-two status, debugging, and metrics.
- [API Stability](concepts/api-stability.md) explains the pre-stable clean-break
  policy and the public/runtime API split.
- [Competitive Positioning](adr/0012-competitive-positioning.md) explains where
  Kapro fits beside Sveltos, Argo Rollouts, Flagger, and GitOps Toolkit.

## Core Objects

| Kind | Role |
|---|---|
| `DeliveryUnit` | App/workload source mappings, trigger intent, and default fleet/plan. |
| `Fleet` | Target set: clusters and delivery defaults. |
| `Source` | Controller-derived source mapping object from a DeliveryUnit. |
| `Substrate` | Delivery driver configuration for Flux, Argo CD, OCI, direct apply, or plugin-backed execution. |
| `Plan` | Stage order, target selection, and gates. |
| `Promotion` | Explicit rollout action. |
| `PromotionRun` | Controller-authored execution attempt and audit record. |
| `Target` | Per-cluster, per-stage runtime state. |
| `Cluster` | A workload cluster known to the hub. |
| `Approval` | Human approval or rejection for a gated target. |

Kapro is pre-stable public release software. User-authored APIs live in
`kapro.io/v1alpha1`; controller-owned runtime records live in
`runtime.kapro.io/v1alpha1`.
