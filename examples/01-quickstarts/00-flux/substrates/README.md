# Flux Substrate

Defines the Flux substrate used by `00-flux/`.

```text
SubstrateClass -> FluxSubstrateConfig -> Substrate
```

Apply before the DeliveryUnit, Plan, Fleet, and Promotion:

```bash
kubectl apply -f examples/01-quickstarts/00-flux/substrates/flux.yaml
```
