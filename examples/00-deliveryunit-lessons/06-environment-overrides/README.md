# 06 Environment Overrides

Overrides keep common defaults in one place while changing values for a stage,
cluster list, or unit.

```text
base values + production selector override -> rendered unit values
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/06-environment-overrides/
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/00-deliveryunit-lessons/06-environment-overrides/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/00-deliveryunit-lessons/06-environment-overrides/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/00-deliveryunit-lessons/06-environment-overrides --ignore-not-found
```
