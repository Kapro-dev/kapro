# Demo Operator Overlay

Kustomize overlay for running Kapro in the Kind demo.

```text
kustomization.yaml -> RBAC + manager env patch
```

Apply with:

```bash
kubectl apply -k examples/10-kind-demo/operator
```
