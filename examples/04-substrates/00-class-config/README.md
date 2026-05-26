# Substrate Class And Typed Config Examples

These examples show the v1alpha1 substrate class/config path.

`SubstrateClass` is the admin-owned capability object. `Substrate` remains the
configured delivery instance that `Fleet`, `Cluster`, and `Promotion` objects
already reference through `spec.substrate.ref`.

The operator writes `SubstrateClass.status`; do not copy status fields into
Git. Apply the class and config manifests, enable the `substrateclass` and
`substrate` controllers, then wait for `Accepted=True` on the class and
`Ready=True` on the substrate.

```sh
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system --create-namespace \
  --set controllers='{deliveryunit,fleet,plan,promotion,promotionrun,cluster,substrateclass,substrate}'
```

Start small with `kubernetes-apply` for Gitless local testing, then move to
`argo`, `flux`, or `oci` as the delivery substrate becomes part of your
platform.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/04-substrates/00-class-config/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/04-substrates/00-class-config/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/04-substrates/00-class-config --ignore-not-found
```
