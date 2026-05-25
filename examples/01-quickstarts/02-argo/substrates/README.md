# Argo CD Substrate

Defines the Argo CD substrate used by `02-argo/`.

```text
SubstrateClass -> ArgoCDSubstrateConfig -> Substrate
```

Apply before the DeliveryUnit, Plan, Fleet, and Promotion:

```bash
kubectl apply -f examples/01-quickstarts/02-argo/substrates/argo.yaml
```
