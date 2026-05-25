# Argo CD Quickstart

Use this path when Argo CD already owns one `Application` per target cluster
and Kapro should promote by updating `spec.source.targetRevision`.

Artifact input: Argo CD must be able to sync the repository and revision named
by each `Application`. Kapro changes the revision; Argo CD performs the sync.
OCI is only involved if your Argo CD Application source uses OCI.

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
kubectl apply -f examples/01-quickstarts/02-argo/substrates/argo.yaml
kubectl apply -f examples/01-quickstarts/02-argo/deliveryunit.yaml
kubectl apply -f examples/01-quickstarts/02-argo/plan.yaml
kubectl apply -f examples/01-quickstarts/02-argo/fleet.yaml
kubectl apply -f examples/01-quickstarts/02-argo/promotion.yaml
kubectl get promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
```

The example expects Argo CD `Application` objects named
`checkout-argo-canary` and `checkout-argo-production` in the `argocd`
namespace. By default, Kapro maps each target to the Application with the same
name as that target. Those Applications must opt in to Kapro writes with one of
these labels or annotations:

```yaml
kapro.io/managed-by: kapro
kapro.io/authorized-source: "*"
kapro.io/authorized-unit: checkout-argo
```

Use a global `spec.substrate.parameters.application` only for single-target
demos. For different Application names per cluster, use standalone `Cluster`
objects with their own delivery parameters instead of the inline cluster list.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/02-argo/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/01-quickstarts/02-argo/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/01-quickstarts/02-argo --ignore-not-found
```
