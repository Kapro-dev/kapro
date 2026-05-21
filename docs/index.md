# Kapro

Kapro is a promotion control plane for Kubernetes fleets. It coordinates when
an artifact version should move across clusters while Flux, Argo CD, OCI pull
agents, and other delivery systems keep owning local rollout mechanics.

Kapro answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

## Start Here

- [Concepts](concepts.md) explains the API objects and lifecycle.
- [Install](install.md) shows the supported operator install paths.
- [First Promotion in 10 Minutes](first-promotion-10min.md) is the shortest
  working path.
- [Backends](backends.md) explains Flux, Argo CD, OCI, and plugin delivery
  options.
- [Operations](operations.md) covers day-two status, debugging, and metrics.

## Core Objects

| Kind | Role |
|---|---|
| `Fleet` | Fleet setup root: source, delivery defaults, clusters, and embedded stage plan. |
| `Source` | Reusable catalog of deployable units and backend write targets. |
| `Promotion` | User-authored rollout intent. |
| `PromotionRun` | Controller-authored execution attempt and audit record. |
| `Target` | Per-cluster, per-stage runtime state. |
| `Cluster` | A workload cluster known to the hub. |
| `Approval` | Human approval or rejection for a gated target. |

Kapro is pre-stable public release software. The current Kubernetes API group is
`kapro.io/v1alpha2`.
