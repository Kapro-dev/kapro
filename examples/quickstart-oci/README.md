# OCI Quickstart

Use this path when spokes should pull OCI artifacts directly with Kapro's
spoke-side OCI delivery core, without requiring Flux or Argo CD on the spoke.

```bash
kubectl apply -f examples/quickstart-oci/backend-oci.yaml
kubectl apply -f examples/quickstart-oci/fleet.yaml
kubectl apply -f examples/quickstart-oci/promotion.yaml
kubectl get promotions,promotionruns,targets
```

The example uses anonymous `ghcr.io/example/...` placeholders. Replace
`spec.delivery.parameters.repository` and registry settings with the OCI
registry that contains your rendered workload artifacts.
