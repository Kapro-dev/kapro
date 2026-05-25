# OCI Quickstart

Use this path when spokes should pull OCI artifacts directly with Kapro's
spoke-side OCI delivery core, without requiring Flux or Argo CD on the spoke.

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
kubectl apply -f examples/01-quickstarts/03-oci/substrates/oci.yaml
kubectl apply -f examples/01-quickstarts/03-oci/deliveryunit.yaml
kubectl apply -f examples/01-quickstarts/03-oci/plan.yaml
kubectl apply -f examples/01-quickstarts/03-oci/fleet.yaml
kubectl apply -f examples/01-quickstarts/03-oci/promotion.yaml
kubectl get promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
```

The example uses anonymous `ghcr.io/example/...` placeholders. Replace
`spec.substrate.parameters.repository` and registry settings with registries
your spokes can reach.

OCI fields have different jobs:

- `DeliveryUnit.spec.source.registries` is where chart or source units come from.
- `substrate.parameters.repository` is what spoke-side OCI delivery pulls.
