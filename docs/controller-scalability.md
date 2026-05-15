# Controller Scalability and Resilience

Kapro is a hub-side controller system. It scales by keeping release state in
Kubernetes, bounding controller retries, and partitioning large fleets across
controller shards when a single workqueue is no longer enough.

## Control Plane Assumptions

| Area | Assumption |
|---|---|
| State | `Release`, `ReleaseTarget`, `MemberCluster`, and `Approval` status are the durable source of truth. Controller memory is disposable. |
| Concurrency | The manager default is `MaxConcurrentReconciles=5`. Per-controller queue workers use that default unless overridden. |
| Workqueues | Release and ReleaseTarget controllers use exponential failure rate limiters. |
| Retry backoff | Release failures back off from 50 ms to 10 minutes. ReleaseTarget failures back off from 50 ms to 5 minutes. |
| Normal polling | Stable external waits use 30 seconds unless a gate returns a shorter `retryAfter`. |
| Slow waits | Long external waits use 2 minutes where the FSM classifies the wait as slow. |
| Leader election | Enabled for production and required when `replicaCount > 1`. |
| Runtime plugins | Startup-time only for actuator and gate dispatch. Dynamic reload is not part of the current scaling model. |

## Rate Limits

Controller-runtime workqueues protect the API server from tight failure loops.
They are not a substitute for backend-specific rate limits. External actuators,
gates, planners, and webhook receivers must enforce their own client-side
timeouts and backend quotas.

Operational defaults:

- Keep plugin RPC deadlines at or below the `PluginRegistration.spec.timeout`
  value.
- Use gate `retryAfter` values to slow repeated checks against Prometheus,
  policy engines, and external APIs.
- Keep `Stage.spec.strategy.maxParallel` below the backend's safe write
  capacity for the selected target set.
- Prefer multiple stages over one very large stage when backend APIs have
  strict per-tenant write quotas.

## Sharding

Kapro supports label-based sharding for the Release and ReleaseTarget
controllers.

Set the shard name with:

```yaml
env:
  - name: KAPRO_SHARD
    value: shard-a
```

Assign owned objects with:

```yaml
metadata:
  labels:
    kapro.io/shard: shard-a
```

Shard labels must be applied consistently to `Release` and `ReleaseTarget`
objects. Admission or automation that creates Releases should set
`kapro.io/shard` at creation time so the correct controller instance receives
the first event.

Current sharding scope:

| Controller | Shard-aware |
|---|---|
| Release | Yes |
| ReleaseTarget | Yes |
| Approval | No |
| Kapro | No |
| PluginRegistration | No |
| ReleaseTrigger | No |

For large fleets, run one active operator manager per shard and set
`KAPRO_CONTROLLERS=release,release-target` on shard workers. Shard workers must
not share the same leader-election identity; each active shard needs an
independent leader-election lock. Run the remaining controllers in a separate
singleton manager with leader election enabled.

## Workqueue Behavior

The Release controller owns orchestration:

- resolves pipeline and stage dependencies;
- selects targets;
- creates or updates `ReleaseTarget` objects;
- aggregates child status into `Release.status`.

The ReleaseTarget controller owns per-target progress:

- evaluates gates;
- calls actuators;
- records phase changes and retry timestamps;
- emits lifecycle notifications.

This split keeps high-cardinality per-cluster work out of the top-level
Release reconcile loop. A large stage should produce many independent
ReleaseTarget queue items instead of one long-running Release reconcile.

## Large Fleet Limits

Kapro is designed for fleets in the 10-500 cluster range by default. Larger
fleets require explicit partitioning:

- Use stages and `maxParallel` to cap concurrent target writes.
- Use shard labels when a single ReleaseTarget queue cannot keep up with gate
  or actuator latency.
- Keep target status compact. Store detailed backend evidence in backend
  systems and link to it from status, rather than embedding large payloads.
- Keep gate checks stateless or reconstructable from Kubernetes and backend
  state.
- Keep external plugin servers horizontally scalable behind stable DNS names.
- Monitor API server request latency, workqueue depth, reconcile error rate,
  plugin RPC latency, and notification delivery failures.

A single Release should not be used as an unbounded global fan-out primitive.
Model global rollouts as explicit waves with conservative `maxParallel` values
and clear failure policy.

## Resilience Rules

- Controller restarts must not lose release progress.
- Rollback intent is represented by Kubernetes state and a new reconcile, not
  by in-memory callbacks.
- Approval decisions are persisted as `Approval` objects.
- Gate and actuator implementations must be idempotent because reconcile can
  repeat after any crash, leader change, or API conflict.
- Webhook notifications may be retried. Receivers should de-duplicate using the
  CloudEvents ID when structured CloudEvents are enabled.
