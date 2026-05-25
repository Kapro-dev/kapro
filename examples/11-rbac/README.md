# RBAC Examples

Recommended RBAC surfaces for public-preview installs.

Files:

- `recommended-roles.yaml` defines default operator and user-facing roles.
- `substrate-observe-adopt-roles.yaml` defines observe/adopt permissions for substrate discovery.

Validate with:

```bash
scripts/validate-yaml-json
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/11-rbac/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/11-rbac/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/11-rbac --ignore-not-found
```
