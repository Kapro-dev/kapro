# OCI Substrate

Defines the OCI bundle pull-mode substrate used by `03-oci/`.

```text
SubstrateClass -> OCIBundleApplyConfig -> Substrate
```

Apply before the DeliveryUnit, Plan, Fleet, and Promotion:

```bash
kubectl apply -f examples/01-quickstarts/03-oci/substrates/oci.yaml
```
