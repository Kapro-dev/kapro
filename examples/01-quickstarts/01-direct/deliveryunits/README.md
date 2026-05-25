# Direct DeliveryUnits

DeliveryUnit source intent for the direct-apply quickstart.

The unit maps to the Kubernetes Deployment image field:

```text
DeliveryUnit -> Source unit -> Deployment image field
```

Apply from the repository root:

```bash
kubectl apply -f examples/01-quickstarts/01-direct/deliveryunits
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/01-direct/deliveryunits/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/01-quickstarts/01-direct/deliveryunits/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/01-quickstarts/01-direct/deliveryunits --ignore-not-found
```
