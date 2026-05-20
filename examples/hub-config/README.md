# Kapro Hub Config Example

This is an advanced template for a dedicated Kapro hub config repository using
direct `PromotionRun` compatibility manifests. New first-use paths should start
from `examples/quickstart/`, where users author `Promotion` and the controller
stamps `PromotionRun` attempts.

The nested `.github/workflows/` directory is intended for the copied hub config repository. It is not part of the Kapro source repository CI.

For the source-of-truth model, see [Concepts](../../docs/concepts.md#hub-config-source-of-truth).

## Structure

```
clusters/
  canary-eu.yaml              FleetCluster for the canary spoke
  prod-eu.yaml                FleetCluster for a production EU spoke
  prod-us.yaml                FleetCluster for a production US spoke
backends/
  flux.yaml                   BackendProfile for generated Flux delivery
  flux-observe.yaml           Observe-only Flux brownfield profile
  argo-observe.yaml           Observe-only Argo CD brownfield profile
sources/
  checkout.yaml               PromotionSource unit and registry metadata
promotionplans/
  checkout-progressive.yaml   PromotionPlan stages, selectors, and gates
promotionruns/
  checkout-v1.2.3.yaml        Advanced direct PromotionRun compatibility manifest
.github/workflows/
  apply-kapro-hub-config.yaml Pull request validation and main-branch apply
```

## Usage

1. Copy this directory into a new hub config git repository.
2. Configure hub cluster authentication in `.github/workflows/apply-kapro-hub-config.yaml`.
3. Edit `clusters/`, `backends/`, `sources/`, `promotionplans/`, and
   `promotionruns/` for your fleet.
4. Open a pull request. CI runs server-side validation and `kubectl diff`.
5. Merge to `main`. CI applies the directories to the hub cluster in order.
6. Kapro reconciles the `PromotionRun`, creates selected targets, and the selected backend applies the referenced version.

## Apply Order

Apply configuration in this order:

1. `clusters/` - creates `FleetCluster` inventory and labels.
2. `backends/` - creates selectable `BackendProfile` objects.
3. `sources/` - creates reusable `PromotionSource` metadata.
4. `promotionplans/` - creates reusable rollout stage DAGs.
5. `promotionruns/` - creates advanced direct PromotionRun compatibility objects for immutable versions.

```bash
kubectl apply -f clusters/
kubectl apply -f backends/
kubectl apply -f sources/
kubectl apply -f promotionplans/
kubectl apply -f promotionruns/
```

## Validation Commands

Run these commands against a hub cluster with the Kapro CRDs installed:

```bash
kubectl apply --dry-run=server -f clusters/
kubectl apply --dry-run=server -f backends/
kubectl apply --dry-run=server -f sources/
kubectl apply --dry-run=server -f promotionplans/
kubectl apply --dry-run=server -f promotionruns/

kubectl diff -f clusters/ || true
kubectl diff -f backends/ || true
kubectl diff -f sources/ || true
kubectl diff -f promotionplans/ || true
kubectl diff -f promotionruns/ || true
```

After apply:

```bash
kubectl get fleetclusters.kapro.io
kubectl get promotionruns.kapro.io,promotiontargets.kapro.io
kubectl describe promotionruns.kapro.io checkout-v1-2-3
```

## Ownership Boundaries

Keep generated runtime state out of this repository. Do not commit spoke workloads, packaged source contents, controller status, kubeconfigs, or raw secrets here.

Secrets should come from the platform's secret-management path, such as External Secrets Operator or sealed secrets. Infrastructure should remain in the infrastructure repository.
