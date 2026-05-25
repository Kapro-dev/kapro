# 00 Flux Quickstart

Default quickstart for GitOps users. Kapro coordinates promotion intent and
Flux reconciles workload state.

```text
Promotion -> Fleet -> Flux substrate -> Flux-managed clusters
```

Apply in order:

```bash
kubectl apply -f examples/01-quickstarts/00-flux/substrates/flux.yaml
kubectl apply -f examples/01-quickstarts/00-flux/deliveryunit.yaml
kubectl apply -f examples/01-quickstarts/00-flux/plan.yaml
kubectl apply -f examples/01-quickstarts/00-flux/kapro.yaml
kubectl apply -f examples/01-quickstarts/00-flux/promotion.yaml
```
