# Kapro Hub Config Example

This is a template for a dedicated Kapro hub config repository. Copy the contents of this directory into that repository and let CI validate and apply changes to the hub cluster.

The nested `.github/workflows/` directory is intended for the copied hub config repository. It is not part of the Kapro source repository CI.

For the source-of-truth model, see [docs/hub-config-source-of-truth.md](../../docs/hub-config-source-of-truth.md).

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
policies/
  checkout-prod-guardrails.yaml PromotionPolicy guardrails for Promotion intent
promotionplans/
  checkout-progressive.yaml   PromotionPlan stages, selectors, and gates
promotions/
  checkout-v1.2.3.yaml        Promotion intent for one immutable artifact
promotionruns/
  checkout-v1.2.3.yaml        Direct PromotionRun compatibility example
.github/workflows/
  apply-kapro-hub-config.yaml Pull request validation and main-branch apply
```

## Usage

1. Copy this directory into a new hub config git repository.
2. Configure hub cluster authentication in `.github/workflows/apply-kapro-hub-config.yaml`.
3. Edit `clusters/`, `backends/`, `sources/`, `policies/`,
   `promotionplans/`, and `promotions/` for your fleet. The `promotionruns/`
   directory is a direct-CRD compatibility example and is not part of the
   standard Promotion workflow.
4. Open a pull request. CI runs server-side validation and `kubectl diff`.
5. Merge to `main`. CI applies the directories to the hub cluster in order.
6. Kapro reconciles the `Promotion`, creates a `PromotionRun`, and the selected backend applies the referenced version.

## Apply Order

Apply configuration in this order:

1. `clusters/` - creates `FleetCluster` inventory and labels.
2. `backends/` - creates selectable `BackendProfile` objects.
3. `sources/` - creates reusable `PromotionSource` metadata.
4. `policies/` - creates reusable `PromotionPolicy` guardrails.
5. `promotionplans/` - creates reusable rollout stage DAGs.
6. `promotions/` - creates user-facing Promotion intent that references
   source, policy, and plans.

The `promotionruns/` directory is intentionally excluded from the standard
apply loop. Use it only when you want to bypass `Promotion` and manage
`PromotionRun` objects directly; in that case, replace the `promotions/` step
with `promotionruns/` for that repository.

```bash
kubectl apply -f clusters/
kubectl apply -f backends/
kubectl apply -f sources/
kubectl apply -f policies/
kubectl apply -f promotionplans/
kubectl apply -f promotions/
```

## Validation Commands

Run these commands against a hub cluster with the Kapro CRDs installed:

```bash
kubectl apply --dry-run=server -f clusters/
kubectl apply --dry-run=server -f backends/
kubectl apply --dry-run=server -f sources/
kubectl apply --dry-run=server -f policies/
kubectl apply --dry-run=server -f promotionplans/
kubectl apply --dry-run=server -f promotions/

kubectl diff -f clusters/ || true
kubectl diff -f backends/ || true
kubectl diff -f sources/ || true
kubectl diff -f policies/ || true
kubectl diff -f promotionplans/ || true
kubectl diff -f promotions/ || true
```

After apply:

```bash
kubectl get fleetclusters.kapro.io
kubectl get promotions.kapro.io,promotionruns.kapro.io,promotiontargets.kapro.io
kubectl describe promotions.kapro.io checkout-v1-2-3
```

## Ownership Boundaries

Keep generated runtime state out of this repository. Do not commit spoke workloads, packaged source contents, controller status, kubeconfigs, or raw secrets here.

Secrets should come from the platform's secret-management path, such as External Secrets Operator or sealed secrets. Infrastructure should remain in the infrastructure repository.
