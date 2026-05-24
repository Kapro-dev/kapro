# Substrate Class And Typed Config Examples

These examples show the v1alpha1 substrate class/config path.

`SubstrateClass` is the admin-owned capability object. `Substrate` remains the
configured delivery instance that `Fleet`, `Cluster`, and `Promotion` objects
already reference through `delivery.substrateRef`.

The operator writes `SubstrateClass.status`; do not copy status fields into
Git. Apply the class and config manifests, enable the `substrateclass` and
`substrate` controllers, then wait for `Accepted=True` on the class and
`Ready=True` on the substrate.

```sh
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system --create-namespace \
  --set controllers='{fleet,plan,promotion,promotionrun,cluster,substrateclass,substrate}'
```

Start small with `kubernetes-apply` for Gitless local testing, then move to
`argo`, `flux`, or `oci` as the delivery substrate becomes part of your
platform.
