# OCI Quickstart

This quickstart uses Kapro's spoke-side OCI delivery core. Spokes pull rendered
OCI artifacts and apply them directly, so this path does not require Flux or
Argo CD on target clusters.

For a generated repo shape, start with:

```bash
kapro create oci ./promotion-repo --name checkout
cd promotion-repo
kubectl apply -f substrates/oci.yaml
kubectl wait --for=condition=Ready substrate/oci --timeout=90s
kubectl apply --recursive -f clusters -f deliveryunits -f plans -f fleets -f promotions
```

The checked-in static example below is useful for smoke tests and release
verification.

Prerequisites for a real OCI pull deployment:

- Kapro operator installed on the hub with the `cluster-bootstrap` controller
  enabled and `hubAPIURL` set for spoke reachability.
- `kapro-cluster-controller` installed on each spoke. See
  [Registering a Cluster (Pull Mode)](../operations/cluster-bootstrap.md) for the bootstrap
  flow and required chart values.
- OCI artifacts published for each promoted unit.
- A clone of this repository, because the commands below apply manifests from
  `examples/quickstart-oci/`.

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
kubectl apply -f examples/quickstart-oci/substrates/oci.yaml
kubectl apply -f examples/quickstart-oci/deliveryunit.yaml
kubectl apply -f examples/quickstart-oci/plan.yaml
kubectl apply -f examples/quickstart-oci/fleet.yaml
kubectl apply -f examples/quickstart-oci/promotion.yaml
kubectl get promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
```

Replace the placeholder `ghcr.io/example/...` repositories with your workload
artifact registry before using this outside a demo cluster.
