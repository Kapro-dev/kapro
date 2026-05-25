# 07 Promote Per-Unit Versions

Use `spec.versions` when each unit needs its own target version.

```text
Promotion.versions.web    -> web unit
Promotion.versions.worker -> worker unit
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/07-promote-per-unit-versions/
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/00-deliveryunit-lessons/07-promote-per-unit-versions/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/00-deliveryunit-lessons/07-promote-per-unit-versions/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/00-deliveryunit-lessons/07-promote-per-unit-versions --ignore-not-found
```
