# Direct Plans

Rollout strategy for the direct-apply quickstart.

```text
canary stage -> production stage
```

The Promotion references this Plan through `planRef`.

Apply from the repository root:

```bash
kubectl apply -f examples/01-quickstarts/01-direct/plans
```
