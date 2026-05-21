# OCI Quickstart

This quickstart uses Kapro's spoke-side OCI delivery core. Spokes pull rendered
OCI artifacts and apply them directly, so this path does not require Flux or
Argo CD on target clusters.

Prerequisites:

- Kapro operator installed on the hub with the `cluster-bootstrap` controller
  enabled and `hubAPIURL` set for spoke reachability.
- `kapro-cluster-controller` installed on each spoke. See
  [Registering a Cluster (Pull Mode)](cluster-bootstrap.md) for the bootstrap
  flow and required chart values.
- OCI artifacts published for each promoted unit.

```bash
kubectl apply -f examples/quickstart-oci/backend-oci.yaml
kubectl apply -f examples/quickstart-oci/fleet.yaml
kubectl apply -f examples/quickstart-oci/promotion.yaml
kubectl get promotions,promotionruns,targets
```

Replace the placeholder `ghcr.io/example/...` repositories with your workload
artifact registry before using this outside a demo cluster.
