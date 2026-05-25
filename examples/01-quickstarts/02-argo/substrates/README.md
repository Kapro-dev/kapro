# Argo CD Substrate

Defines the Argo CD substrate used by `02-argo/`.

```text
SubstrateClass -> ArgoCDSubstrateConfig -> Substrate
```

Apply before the DeliveryUnit, Plan, Fleet, and Promotion:

```bash
kubectl apply -f examples/01-quickstarts/02-argo/substrates/argo.yaml
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/02-argo/substrates/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/01-quickstarts/02-argo/substrates/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/01-quickstarts/02-argo/substrates --ignore-not-found
```
