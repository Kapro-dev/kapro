# Kapro

Kapro is a promotion control plane for Kubernetes fleets. It coordinates when
an artifact version should move across clusters while Flux, Argo CD, OCI pull
agents, and other delivery systems keep owning local rollout mechanics.

Kapro answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

## Start Here

- [Concepts](concepts/concepts.md) explains the API objects and lifecycle.
- [Install](getting-started/install.md) shows the supported operator install paths.
- [Adoption Guide](getting-started/adoption.md) helps choose greenfield, Argo brownfield,
  Flux brownfield, or OCI pull mode.
- [Adoption CLI](getting-started/adoption-cli.md) shows the guided `kapro quickstart`,
  `kapro sample`, `kapro doctor`, and `kapro explain` paths.
- [First Promotion in 10 Minutes](getting-started/first-promotion-10min.md) is the shortest
  working path.
- [Backends](concepts/backends.md) explains Flux, Argo CD, OCI, and plugin delivery
  options.
- [Operations](operations/operations.md) covers day-two status, debugging, and metrics.
- [v1alpha1 to v1alpha2 Migration](migration/migration-v1alpha1-to-v1alpha2.md) explains
  the clean-break upgrade path for legacy alpha manifests.
- [Competitive Positioning](adr/0012-competitive-positioning.md) explains where
  Kapro fits beside Sveltos, Argo Rollouts, Flagger, and GitOps Toolkit.

## Core Objects

| Kind | Role |
|---|---|
| `Fleet` | Fleet setup root: source, delivery defaults, clusters, and embedded stage plan. |
| `Source` | Reusable catalog of deployable units and backend write targets. |
| `Backend` | Delivery driver configuration for Flux, Argo CD, OCI, or plugin-backed execution. |
| `Plan` | Stage order, target selection, and gates generated from or referenced by a Fleet. |
| `Promotion` | User-authored rollout intent. |
| `PromotionRun` | Controller-authored execution attempt and audit record. |
| `Target` | Per-cluster, per-stage runtime state. |
| `Cluster` | A workload cluster known to the hub. |
| `Approval` | Human approval or rejection for a gated target. |

Kapro is pre-stable public release software. The current Kubernetes API group is
`kapro.io/v1alpha2`.
