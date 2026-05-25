# 00 Flux Quickstart

Default quickstart for GitOps users. Kapro coordinates promotion intent and
Flux reconciles workload state.

```text
Promotion -> Fleet -> Flux substrate -> Flux-managed clusters
```

This path does not require Kapro to pull OCI bundles. It assumes Flux can reach
the Git or Flux source objects referenced by the workload configuration. If your
Flux source is an OCIRepository, seed that registry separately.

Apply in order:

```bash
kubectl apply -f examples/01-quickstarts/00-flux/substrates/flux.yaml
kubectl apply -f examples/01-quickstarts/00-flux/deliveryunit.yaml
kubectl apply -f examples/01-quickstarts/00-flux/plan.yaml
kubectl apply -f examples/01-quickstarts/00-flux/kapro.yaml
kubectl apply -f examples/01-quickstarts/00-flux/promotion.yaml
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/00-flux/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/01-quickstarts/00-flux/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/01-quickstarts/00-flux --ignore-not-found
```
