# CloudEvents Printer Manifests

Deployment and Service for the CloudEvents printer example.

```text
Kapro lifecycle events -> Service -> cloudevents-printer logs
```

Apply into the namespace that should receive events:

```bash
kubectl apply -n kapro-events -f examples/05-plugins/05-cloudevents-printer/manifests/deployment.yaml
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/05-plugins/05-cloudevents-printer/manifests/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/05-plugins/05-cloudevents-printer/manifests/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/05-plugins/05-cloudevents-printer/manifests --ignore-not-found
```
