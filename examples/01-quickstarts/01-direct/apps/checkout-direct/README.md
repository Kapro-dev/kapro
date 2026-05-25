# Checkout Direct Workload

Minimal Kubernetes workload used by the direct-apply quickstart.

Files:

- `deployment.yaml` defines the app container Kapro updates.
- `service.yaml` exposes the app inside the cluster.

Apply from the repository root:

```bash
kubectl apply -f examples/01-quickstarts/01-direct/apps/checkout-direct/deployment.yaml
kubectl apply -f examples/01-quickstarts/01-direct/apps/checkout-direct/service.yaml
```
