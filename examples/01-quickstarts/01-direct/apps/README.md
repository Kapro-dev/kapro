# Direct App Manifests

Starter Kubernetes workload manifests for the direct-apply quickstart.

```text
apps/checkout-direct -> Deployment + Service
```

Apply with the rest of the direct quickstart:

```bash
kubectl apply --recursive -f examples/01-quickstarts/01-direct/apps
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/01-direct/apps/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/01-quickstarts/01-direct/apps/checkout-direct/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
