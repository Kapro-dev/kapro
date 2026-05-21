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
namespace. By default, Kapro maps each target to the Application with the same
name as that target. Those Applications must opt in to Kapro writes with one of
these labels or annotations:

```yaml
kapro.io/managed-by: kapro
kapro.io/authorized-source: "*"
kapro.io/authorized-unit: checkout
```

Use a global `spec.delivery.parameters.application` only for single-target
demos. For different Application names per cluster, use standalone `Cluster`
objects with their own delivery parameters instead of the inline cluster list.
