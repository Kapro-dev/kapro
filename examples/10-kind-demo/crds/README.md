# Demo CRDs

Fixture CRDs used by the Kind demo to model external resources.

Apply before fixture instances:

```bash
kubectl apply -f examples/10-kind-demo/crds
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/10-kind-demo/crds/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/10-kind-demo/crds/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/10-kind-demo/crds --ignore-not-found
```
