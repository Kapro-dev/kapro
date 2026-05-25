# OCI Substrate

Defines the OCI bundle pull-mode substrate used by `03-oci/`.

```text
SubstrateClass -> OCIBundleApplyConfig -> Substrate
```

Apply before the DeliveryUnit, Plan, Fleet, and Promotion:

```bash
kubectl apply -f examples/01-quickstarts/03-oci/substrates/oci.yaml
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/03-oci/substrates/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/01-quickstarts/03-oci/substrates/run.sh apply
```

For OCI-backed lessons, seed a local registry artifact before applying resources that reference registry content:

```bash
examples/01-quickstarts/03-oci/substrates/run.sh oci-prep
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.
- `oci-prep` starts or reuses a local zot registry on `localhost:5001` and pushes a small ORAS artifact.

## Cleanup

```bash
kubectl delete -f examples/01-quickstarts/03-oci/substrates --ignore-not-found
```
