# OCI Quickstart

Use this path when spokes should pull OCI artifacts directly with Kapro's
spoke-side OCI delivery core, without requiring Flux or Argo CD on the spoke.

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
kubectl apply -f examples/quickstart-oci/backend-oci.yaml
kubectl apply -f examples/quickstart-oci/fleet.yaml
kubectl apply -f examples/quickstart-oci/promotion.yaml
kubectl get promotions,promotionruns,targets
```

The example uses anonymous `ghcr.io/example/...` placeholders. Replace
`spec.delivery.parameters.repository` and registry settings with registries
your spokes can reach.

OCI fields have different jobs:

- `Fleet.spec.source.registries` is where chart or source units come from.
- `Fleet.spec.registry` is where Kapro-generated bundles can be published.
- `delivery.parameters.repository` is what spoke-side OCI delivery pulls.
