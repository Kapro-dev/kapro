<p align="center">
  <img src="docs/logo.png" alt="Kapro logo" width="260">
</p>

<h1 align="center">Kapro</h1>

<p align="center"><strong>A promotion control plane for Kubernetes fleets.</strong><br>
Kapro coordinates when an artifact version should move across clusters while
Flux, Argo CD, OCI pull agents, and other delivery systems keep owning the local rollout.</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
  <a href="https://goreportcard.com/report/kapro.io/kapro"><img src="https://goreportcard.com/badge/kapro.io/kapro" alt="Go Report Card"></a>
  <a href="api/v1alpha2"><img src="https://img.shields.io/badge/API-kapro.io%2Fv1alpha2-purple" alt="API Group"></a>
</p>

---

Kapro is **pre-stable public release software**, not GA. The current public
release line is `v0.1.0`; all Kubernetes APIs are now `kapro.io/v1alpha2`.

## What Kapro Does

Kapro answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

It is useful when one application version must move through many clusters,
regions, environments, or connectivity models without putting all promotion
logic into CI scripts.

Kapro owns:

- cross-cluster promotion intent;
- stage and wave ordering;
- target selection;
- gate and approval state;
- per-target execution records;
- backend convergence evidence.

Kapro does not build artifacts, render manifests, replace GitOps controllers,
or implement in-cluster traffic shifting. Those jobs stay with CI, Helm,
Kustomize, Flux, Argo CD, Argo Rollouts, service mesh controllers, or custom
platform tooling.

## Core Concepts

| Kind | Role |
|---|---|
| `Kapro` | Fleet setup root: source, delivery defaults, clusters, and embedded stage plan. |
| `PromotionSource` | Reusable catalog of deployable units and backend write targets. |
| `Promotion` | User-authored rollout intent: "promote this version through this Kapro fleet." |
| `PromotionRun` | Controller-authored execution attempt and audit record. |
| `PromotionTarget` | Per-cluster, per-stage runtime state. |
| `FleetCluster` | A workload cluster known to the hub. |
| `Approval` | Human approval or rejection for a gated target. |

See [Concepts](docs/concepts.md) for the object model and lifecycle.

## Adapt To Your Fleet

Kapro is backend-neutral. A fleet can mix delivery styles by cluster:

- **Flux or Argo CD brownfield:** discover existing apps first, review the
  generated mappings, then opt selected objects into managed promotion.
- **OCI pull mode:** spoke clusters pull artifacts from inside their own network
  boundary and report status back to the hub.
- **Hub push mode:** the hub patches a backend object directly when network and
  RBAC policy allow it.
- **Plugins:** custom actuators, gates, and planners can be loaded through
  `PluginRegistration` after they pass the conformance harness.

Start with [Backends](docs/backends.md) when deciding how Kapro should connect
to existing delivery systems.

## Quick Start

Install the operator:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
```

Apply a minimal source and fleet setup:

```bash
kubectl apply -f examples/quickstart/backend-flux.yaml
kubectl apply -f examples/quickstart/kapro.yaml
```

Promote a version:

```bash
kubectl apply -f examples/quickstart/promotion.yaml
kubectl get promotions,promotionruns,promotiontargets
```

For a step-by-step minimal path, use [First Promotion in 10 Minutes](docs/first-promotion-10min.md).
For a complete local walkthrough, use the [Kind demo](examples/kind-demo/README.md).

## Documentation

Start here:

- [Concepts](docs/concepts.md)
- [Install](docs/install.md)
- [First Promotion in 10 Minutes](docs/first-promotion-10min.md)
- [Kind Demo](examples/kind-demo/README.md)
- [Backends](docs/backends.md)
- [Operations](docs/operations.md)
- [Security](docs/security.md)
- [API Stability](docs/api-stability.md)
- [Changelog](CHANGELOG.md)

Deeper references:

- [Argo Brownfield Migration](docs/argo-migration.md)
- [Flux Brownfield Migration](docs/flux-migration.md)
- [RBAC and Tenancy](docs/rbac-tenancy.md)
- [Monitoring](docs/monitoring.md)
- [Extension Model](docs/extension-model.md)
- [Plugin Authoring](docs/plugin-authoring.md)
- [Architecture Decision Records](docs/adr/README.md)

## Contributing

Issues and pull requests are welcome. Keep changes tied to implemented behavior:
public docs should describe what users can run today, while larger design
decisions belong in [ADRs](docs/adr/README.md).

## License

Apache 2.0. See [LICENSE](LICENSE).
