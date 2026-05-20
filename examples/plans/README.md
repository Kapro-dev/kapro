# PromotionPlan reference library

Six copy-paste-ready `PromotionPlan` examples covering the most common
progressive-delivery shapes. Each plan is self-contained: apply the
YAML, label your `FleetCluster` objects with the matching keys, and
reference the plan from `Promotion.spec.promotionPlans[].promotionPlan`.

| Plan | Shape | When to use |
|---|---|---|
| [`01-canary-then-full.yaml`](01-canary-then-full.yaml) | canary → prod | Default. One canary, then everything else when canary soaks clean. |
| [`02-blue-green.yaml`](02-blue-green.yaml) | green (idle) → swap | Single-cluster blue/green with manual cutover gate. |
| [`03-multi-region-staggered.yaml`](03-multi-region-staggered.yaml) | EU → US → APAC | Roll one region at a time with cross-region soak time. |
| [`04-region-failover.yaml`](04-region-failover.yaml) | primary → standby (depends on `all`) | Standby promotes only after the entire primary stage holds for 30m. |
| [`05-ring-deployment.yaml`](05-ring-deployment.yaml) | ring0 → ring1 → ring2 → ring3 | Microsoft-shaped concentric rings with parallelism increasing per ring. |
| [`06-metrics-gated.yaml`](06-metrics-gated.yaml) | canary with PromQL gate | Canary must hold `error_rate < 1%` over a 10-min window before prod. |

## How to apply one

```bash
kubectl apply -f examples/plans/01-canary-then-full.yaml

# label your clusters so the stage selectors find them
kubectl label fleetcluster eu-canary    kapro.io/tier=canary
kubectl label fleetcluster eu-prod-{1,2,3} kapro.io/tier=production

# then promote through the plan
kapro promote checkout --version v1.2.3 --plan canary-then-full
```

## Conventions used in this library

- **Selectors** match the label `kapro.io/tier=<value>`. Adjust to your
  fleet's labeling scheme.
- **Soak time** uses `dependsOn[].requiredSoakTime` for the simple
  "wait N minutes before advancing" pattern instead of a full
  `gate.gateTimeout`. Reach for the heavier `gate:` block only when you
  need metrics, webhooks, or approvals.
- **Approvals** use `gate.mode: manual` with `approval.required: true`
  and a stable approver list. Wire approvers to the same Kubernetes
  user/group your Approval webhook already validates.
- **Names** are short and verb-shaped (`canary`, `prod`, `ring0`) so
  `kapro diag` columns stay narrow.

## Building your own

These plans are flat DAGs of `Stage` nodes. Each stage declares:

- `selector` — which clusters the stage targets
- `dependsOn` — upstream stages that must converge first
- `strategy.maxParallel` — how many clusters in this stage roll concurrently
- `gate` — optional convergence check (metrics, soak, approval, webhook)
- `onFailure` — `halt` (default), `skip`, or `rollback`

Validation runs at admission time: stage names must be unique, `dependsOn`
entries must reference real stages, and the DAG must be acyclic.

See [`docs/concepts.md`](../../docs/concepts.md) for the full field
reference and [`docs/first-promotion-10min.md`](../../docs/first-promotion-10min.md)
for an end-to-end walkthrough.
