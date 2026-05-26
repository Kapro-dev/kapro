# OCI Quickstart

Use this path when spokes should pull OCI artifacts directly with Kapro's
spoke-side OCI delivery core, without requiring Flux or Argo CD on the spoke.

Artifact input: an OCI bundle artifact must exist in the registry path used by
`spec.delivery.parameters.repository`.

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
echo "checkout bundle" > checkout.txt
oras push --plain-http localhost:5001/checkout/checkout:v1.2.3 \
  --artifact-type application/vnd.kapro.bundle \
  checkout.txt:text/plain
kubectl apply -f examples/01-quickstarts/03-oci/substrates/oci.yaml
kubectl apply -f examples/01-quickstarts/03-oci/deliveryunit.yaml
kubectl apply -f examples/01-quickstarts/03-oci/plan.yaml
kubectl apply -f examples/01-quickstarts/03-oci/fleet.yaml
kubectl apply -f examples/01-quickstarts/03-oci/promotion.yaml
kubectl get promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
```

The example uses anonymous `ghcr.io/example/...` placeholders. Replace
`spec.delivery.parameters.repository` and registry settings with registries
your spokes can reach.

For local Kind testing, change repository placeholders to the local registry:

```text
localhost:5001 from your laptop
kapro-registry:5000 from inside the Kind network
```

OCI fields have different jobs:

- `DeliveryUnit.spec.source.registries` is where chart or source units come from.
- `delivery.parameters.repository` is what spoke-side OCI delivery pulls.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/03-oci/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/01-quickstarts/03-oci/run.sh apply
```

For OCI-backed lessons, seed a local registry artifact before applying resources that reference registry content:

```bash
examples/01-quickstarts/03-oci/run.sh oci-prep
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.
- `oci-prep` starts or reuses a local zot registry on `localhost:5001` and pushes a small ORAS artifact.

## Cleanup

```bash
kubectl delete -f examples/01-quickstarts/03-oci --ignore-not-found
```
