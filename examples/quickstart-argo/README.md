# Argo CD Quickstart

Use this path when Argo CD already owns one `Application` per target cluster
and Kapro should promote by updating `spec.source.targetRevision`.

```bash
kubectl apply -f examples/quickstart-argo/backend-argo.yaml
kubectl apply -f examples/quickstart-argo/fleet.yaml
kubectl apply -f examples/quickstart-argo/promotion.yaml
kubectl get promotions,promotionruns,targets
```

The example expects Argo CD `Application` objects named
`checkout-argo-canary` and `checkout-argo-production` in the `argocd`
namespace. Override `spec.delivery.parameters.application` or
`applicationSelector` when your Argo naming differs.
