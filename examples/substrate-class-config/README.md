# Substrate Class And Typed Config Examples

These examples show the v1alpha2 substrate class/config path.

`SubstrateClass` is the admin-owned capability object. `Backend` remains the
configured delivery instance that `Fleet`, `Cluster`, and `Promotion` objects
already reference through `delivery.backendRef`.

The operator writes `SubstrateClass.status`; do not copy status fields into
Git. Apply the class and config manifests, enable the `substrateclass` and
`backend` controllers, then wait for `Accepted=True` on the class and
`Ready=True` on the backend.

```sh
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system --create-namespace \
  --set controllers='{fleet,plan,promotion,promotionrun,cluster,substrateclass,backend}'
```

Start small with `kubernetes-apply` for Gitless local testing, then move to
`argo-cd`, `flux`, or `oci` as the delivery substrate becomes part of your
platform.
