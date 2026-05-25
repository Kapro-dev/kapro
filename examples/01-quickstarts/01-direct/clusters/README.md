# Direct Clusters

Target cluster records for the direct-apply quickstart.

```text
canary-eu -> production-eu
```

The Plan selects these clusters by `kapro.io/stage` labels.

Apply from the repository root:

```bash
kubectl apply -f examples/01-quickstarts/01-direct/clusters
```
