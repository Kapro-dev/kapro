<p align="center">
  <img src="docs/logo.png" alt="Kapro logo" width="260">
</p>

<h1 align="center">Kapro</h1>

<p align="center"><strong>A promotion control plane for Kubernetes fleets.</strong><br>
Kapro coordinates when an artifact version should move across clusters while
Flux, Argo CD, OCI pull agents, and other delivery systems keep owning the local rollout.</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
  <a href="https://github.com/Kapro-dev/kapro/releases/latest"><img src="https://img.shields.io/github/v/release/Kapro-dev/kapro?sort=semver" alt="Latest release"></a>
  <a href="https://github.com/Kapro-dev/kapro/actions/workflows/ci.yml"><img src="https://github.com/Kapro-dev/kapro/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://goreportcard.com/report/kapro.io/kapro"><img src="https://goreportcard.com/badge/kapro.io/kapro" alt="Go Report Card"></a>
  <a href="api/v1alpha2"><img src="https://img.shields.io/badge/API-kapro.io%2Fv1alpha2-purple" alt="API Group"></a>
  <a href="https://kapro.dev"><img src="https://img.shields.io/badge/docs-kapro.dev-0a7" alt="Docs"></a>
</p>

---

Kapro is **pre-stable public release software**, not GA. The current public
preview release is `v0.5.7`; all Kubernetes APIs are now `kapro.io/v1alpha2`.
If you have legacy `kapro.io/v1alpha1` manifests, follow the
[v1alpha1 to v1alpha2 migration guide](docs/migration-v1alpha1-to-v1alpha2.md);
this release does not provide automatic legacy conversion.

## Why Kapro

Kapro answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

It is useful when one application version must move through many clusters,
regions, environments, or connectivity models without burying promotion state in
CI scripts.

- **Fleet-wide promotion intent:** model waves, gates, approvals, and target
  selection as Kubernetes API state.
- **Backend-neutral delivery:** keep Flux, Argo CD, OCI pull agents, and custom
  plugins in charge of local rollout mechanics.
- **Auditable attempts:** inspect durable `Promotion` intent, immutable
  `PromotionRun` attempts, and per-target runtime records after CI has exited.

## Boundaries

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
| `Fleet` | Fleet setup root: source, delivery defaults, clusters, and embedded stage plan. |
| `Source` | Reusable catalog of deployable units and backend write targets. |
| `Backend` | Delivery driver configuration for Flux, Argo CD, OCI, or plugin-backed execution. |
| `Plan` | Stage order, target selection, and gates generated from or referenced by a Fleet. |
| `Promotion` | User-authored rollout intent: "promote this version through this Fleet." |
| `PromotionRun` | Controller-authored execution attempt and audit record. |
| `Target` | Per-cluster, per-stage runtime state. |
| `Cluster` | A workload cluster known to the hub. |
| `Approval` | Human approval or rejection for a gated target. |

See [Concepts](docs/concepts.md) for the object model and lifecycle.

## How It Compares

Kapro is not a replacement for Flux, Argo CD, Argo Rollouts, Flagger, or
Sveltos. It sits above delivery and add-on systems as the promotion layer that
decides when a version may advance across a fleet. See
[ADR-0012: Competitive Positioning](docs/adr/0012-competitive-positioning.md)
for the architectural comparison.

## Adapt To Your Fleet

Kapro is backend-neutral. A fleet can mix delivery styles by cluster:

- **Flux or Argo CD brownfield:** discover existing apps first, review the
  generated mappings, then opt selected objects into managed promotion.
- **OCI pull mode:** spoke clusters pull artifacts from inside their own network
  boundary and report status back to the hub.
- **Hub push mode:** the hub patches a backend object directly when network and
  RBAC policy allow it.
- **Plugins:** custom actuators, gates, and planners can be loaded through
  `Plugin` after they pass the conformance harness.

Run [First Promotion in 10 Minutes](docs/first-promotion-10min.md) first to
see the API lifecycle, then use [Backends](docs/backends.md) when deciding how
Kapro should connect to existing delivery systems.

For guided repository setup, use the source-built bootstrap CLI from `main`
until the bootstrap command is included in a tagged CLI release:

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
make build
export PATH="$PWD/bin:$PATH"
kapro bootstrap guide
kapro bootstrap greenfield ./promotion-repo --backend flux --mode pull --name checkout
kapro bootstrap brownfield argo . --out ./kapro-connect --name checkout
kapro bootstrap brownfield flux . --out ./kapro-connect --name checkout
```

See the [Adoption Guide](docs/adoption.md) for the greenfield and brownfield
decision tree.

## Quick Start

Install the released operator, apply the starter fleet from a source clone, and
inspect the controller-owned runtime records:

```bash
KAPRO_VERSION=0.5.7
git clone --branch "v${KAPRO_VERSION}" https://github.com/Kapro-dev/kapro.git
cd kapro
helm upgrade --install kapro \
  "https://github.com/Kapro-dev/kapro/releases/download/v${KAPRO_VERSION}/kapro-operator-${KAPRO_VERSION}.tgz" \
  --namespace kapro-system \
  --create-namespace \
  --wait
kubectl wait crd/promotions.kapro.io crd/promotionruns.kapro.io crd/targets.kapro.io \
  --for=condition=Established \
  --timeout=60s
kubectl -n kapro-system rollout status deployment/kapro-kapro-operator
kubectl apply -f examples/quickstart/backend-flux.yaml
kubectl apply -f examples/quickstart/kapro.yaml
kubectl apply -f examples/quickstart/promotion.yaml
kubectl get promotions,promotionruns,targets
```

The user-authored object is `Promotion`. `PromotionRun` and `Target` are
controller-owned runtime records for inspection in `kubectl` or k9s. This
starter path proves that the hub API stamps `PromotionRun` and `Target`
records. Real `Complete` / `Converged` status requires a wired delivery backend
or the local CI smoke fixture to report workload health.

To run the same local convergence smoke used by CI, use Docker, Kind, Helm, and
kubectl:

```bash
KAPRO_CI_QUICKSTARTS=flux,argo,oci scripts/ci-kind-smoke.sh
```

For a step-by-step minimal path, use [First Promotion in 10 Minutes](docs/first-promotion-10min.md).
For a complete local walkthrough, use the [Kind demo](examples/kind-demo/README.md).
[Install](docs/install.md) has local-checkout and release-asset variants.

## Documentation

Start at [kapro.dev](https://kapro.dev) or use these repo docs:

- [Concepts](docs/concepts.md)
- [Install](docs/install.md)
- [Adoption Guide](docs/adoption.md)
- [First Promotion in 10 Minutes](docs/first-promotion-10min.md)
- [Kind Demo](examples/kind-demo/README.md)
- [Backends](docs/backends.md)
- [Operations](docs/operations.md)
- [Security](docs/security.md)
- [API Stability](docs/api-stability.md)
- [Competitive Positioning](docs/adr/0012-competitive-positioning.md)
- [v1alpha1 to v1alpha2 Migration](docs/migration-v1alpha1-to-v1alpha2.md)
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

Issues and pull requests are welcome. Keep changes tied to implemented
behavior: public docs should describe what users can run today, while larger
design decisions belong in [ADRs](docs/adr/README.md).

- Open issues and feature requests in
  [GitHub Issues](https://github.com/Kapro-dev/kapro/issues).
- Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.
- Report vulnerabilities through [SECURITY.md](SECURITY.md), not public issues.
- Follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Apache 2.0. See [LICENSE](LICENSE).
