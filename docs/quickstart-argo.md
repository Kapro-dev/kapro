# Argo CD Quickstart

This quickstart keeps Argo CD as the cluster reconciler and uses Kapro as the
promotion layer. Kapro updates the Argo CD `Application` target revision for
each target cluster, then waits for Argo to report `Synced` and `Healthy`.

Prerequisites:

- Kapro operator installed.
- Argo CD installed in the `argocd` namespace.
- One Argo CD `Application` per target cluster, named to match the generated
  Kapro `Cluster` names or selected with `applicationSelector`.

```bash
kubectl apply -f examples/quickstart-argo/backend-argo.yaml
kubectl apply -f examples/quickstart-argo/fleet.yaml
kubectl apply -f examples/quickstart-argo/promotion.yaml
kubectl get promotions,promotionruns,targets
```

The sample Fleet creates `checkout-argo-canary` and
`checkout-argo-production` targets. If your Argo Applications use different
names, set `spec.delivery.parameters.application` for one shared Application or
`applicationSelector` for label selection.
