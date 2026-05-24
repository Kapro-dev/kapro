# Argo CD Quickstart

Use this path when Argo CD already owns one `Application` per target cluster
and Kapro should promote by updating `spec.source.targetRevision`.

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
kubectl apply -f examples/quickstart-argo/substrates/argo.yaml
kubectl apply -f examples/quickstart-argo/fleet.yaml
kubectl apply -f examples/quickstart-argo/promotion.yaml
kubectl get promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
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
