# Demo Operator Overlay

Kustomize overlay for running Kapro in the Kind demo.

```text
kustomization.yaml -> RBAC + manager env patch
```

Apply with:

```bash
kubectl apply -k examples/10-kind-demo/operator
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/10-kind-demo/operator/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/10-kind-demo/operator/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -k` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -k examples/10-kind-demo/operator --ignore-not-found
```
