# Kapro Hub Config Example

This is a template for a dedicated Kapro hub config repository. Copy the contents of this directory into that repository and let CI validate and apply changes to the hub cluster.

The nested `.github/workflows/` directory is intended for the copied hub config repository. It is not part of the Kapro source repository CI.

For the source-of-truth model, see [docs/hub-config-source-of-truth.md](../../docs/hub-config-source-of-truth.md).

## Structure

```
clusters/
  canary-eu.yaml              MemberCluster for the canary spoke
  prod-eu.yaml                MemberCluster for a production EU spoke
  prod-us.yaml                MemberCluster for a production US spoke
apps/
  checkout.yaml               KaproApp component and registry metadata
pipelines/
  checkout-progressive.yaml   Pipeline stages, selectors, and gates
releases/
  checkout-v1.2.3.yaml        Release intent for one immutable OCI bundle
.github/workflows/
  apply-kapro-hub-config.yaml Pull request validation and main-branch apply
```

## Usage

1. Copy this directory into a new hub config git repository.
2. Configure hub cluster authentication in `.github/workflows/apply-kapro-hub-config.yaml`.
3. Edit `clusters/`, `apps/`, `pipelines/`, and `releases/` for your fleet.
4. Open a pull request. CI runs server-side validation and `kubectl diff`.
5. Merge to `main`. CI applies the directories to the hub cluster in order.
6. Kapro reconciles the `Release` and spoke clusters pull the referenced OCI bundle.

## Apply Order

Apply configuration in this order:

1. `clusters/` - creates `MemberCluster` inventory and labels.
2. `apps/` - creates reusable `KaproApp` metadata.
3. `pipelines/` - creates reusable rollout stage DAGs.
4. `releases/` - creates release intent that references the pipeline.

```bash
kubectl apply -f clusters/
kubectl apply -f apps/
kubectl apply -f pipelines/
kubectl apply -f releases/
```

## Validation Commands

Run these commands against a hub cluster with the Kapro CRDs installed:

```bash
kubectl apply --dry-run=server -f clusters/
kubectl apply --dry-run=server -f apps/
kubectl apply --dry-run=server -f pipelines/
kubectl apply --dry-run=server -f releases/

kubectl diff -f clusters/ || true
kubectl diff -f apps/ || true
kubectl diff -f pipelines/ || true
kubectl diff -f releases/ || true
```

After apply:

```bash
kubectl get memberclusters.kapro.io
kubectl get kaproapps.kapro.io,pipelines.kapro.io,releases.kapro.io
kubectl describe releases.kapro.io checkout-v1-2-3
```

## Ownership Boundaries

Keep generated runtime state out of this repository. Do not commit spoke workloads, OCI bundle contents, controller status, kubeconfigs, or raw secrets here.

Secrets should come from the platform's secret-management path, such as External Secrets Operator or sealed secrets. Infrastructure should remain in the infrastructure repository.
