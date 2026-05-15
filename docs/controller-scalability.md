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

The current operator binary sets `MaxConcurrentReconciles=5` at the
controller-runtime manager level. It is not currently exposed as a Helm value or
environment variable. When higher worker counts are needed, prefer sharding
first; raising global concurrency should be a deliberate code or chart change
after measuring API server and backend capacity.

## Scaling Levers

Use these levers in this order:

1. Set conservative `Stage.spec.strategy.maxParallel` values so a stage cannot
   exceed backend write capacity.
2. Keep plugin timeouts short and return gate `retryAfter` values for external
   systems that need slower polling.
3. Scale plugin servers horizontally behind stable DNS names.
4. Split high-cardinality rollout work with `KAPRO_SHARD` when one
   ReleaseTarget queue cannot keep up.
5. Only then consider increasing controller worker concurrency.

The first three levers reduce work. Sharding partitions work. Raising worker
concurrency increases pressure on the Kubernetes API server and external
backends, so it should follow measurement rather than precede it.

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
- Use per-plugin client-side rate limits when a backend has tenant, region, or
  token-level quotas.
- Treat `ReleaseTrigger` cooldown and max-active policy as source-side flood
  protection, not as replacement for stage parallelism.

Rate limits should be layered:

| Layer | Mechanism | Protects |
|---|---|---|
| Source | `ReleaseTrigger` cooldown and max-active policy | Hub from artifact bursts |
| Stage | `Stage.spec.strategy.maxParallel` | Backend write APIs and target fleets |
| Controller | Workqueue backoff and reconcile concurrency | Kubernetes API server |
| Plugin | RPC deadline, retry policy, backend client limiter | External systems |
| Receiver | CloudEvents de-duplication and webhook timeout | Notification consumers |

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

Current deployment constraint: the operator uses a fixed leader-election ID.
Until the deployment surface exposes a distinct leader-election ID per shard,
active shard managers in the same namespace can contend for the same lock. Use a
deployment layout that gives each shard an independent leader-election namespace,
or keep one active high-cardinality manager and use shard labels only when the
operator packaging supports separate locks.

Sharding does not change ownership of non-shard-aware controllers. The
singleton manager should continue to own approvals, Kapro resource generation,
plugin registration status, and release triggers.

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

## Workqueue Tuning

Tune workqueues by changing inputs before changing worker counts:

- Reduce duplicate events by creating Releases with final labels, pipeline
  references, and shard labels already set.
- Keep status payloads compact so status patch conflicts and API server payload
  size stay low.
- Avoid gate templates that poll expensive backends every 30 seconds for every
  target; return longer `retryAfter` values for slow checks.
- Keep actuator `Apply` idempotent and cheap when the desired version is already
  applied.
- Use `maxParallel` to bound the number of ReleaseTargets actively invoking a
  backend, even when many targets are queued.

Signals that a queue needs partitioning or tuning:

- controller-runtime workqueue depth grows while reconcile errors stay low;
- `kapro_controller_reconcile_duration_seconds` increases with fleet size;
- `kapro_controller_status_writes_total{result="error"}` or API conflict
  rates rise during large stages;
- plugin RPC latency approaches `PluginRegistration.spec.timeout`;
- gate evaluations remain `running` or `inconclusive` for many targets at once.

If errors dominate, fix the failing backend or validation path before adding
workers. More workers will make a persistent error loop louder.

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
- Cap simultaneous active Releases per app or tenant with process policy until
  a dedicated quota API exists.
- Use separate hub clusters when regulatory, network, or API-server isolation
  matters more than a single global view.

A single Release should not be used as an unbounded global fan-out primitive.
Model global rollouts as explicit waves with conservative `maxParallel` values
and clear failure policy.

Large-fleet sizing assumptions:

| Dimension | Default assumption | Larger-fleet action |
|---|---|---|
| Targets per Release | Tens to hundreds | Split into stages, shards, or separate Releases |
| Active Releases | Low per app or tenant | Add process quotas and watch active release metrics |
| Target status | Compact status plus backend links | Store evidence outside CRD status |
| Plugin backend latency | Seconds, bounded by timeout | Scale plugin servers and slow polling |
| API server latency | Stable under status update load | Partition hubs or shards before raising workers |

## Resilience Rules

- Controller restarts must not lose release progress.
- Rollback intent is represented by Kubernetes state and a new reconcile, not
  by in-memory callbacks.
- Approval decisions are persisted as `Approval` objects.
- Gate and actuator implementations must be idempotent because reconcile can
  repeat after any crash, leader change, or API conflict.
- Webhook notifications may be retried. Receivers should de-duplicate using the
  CloudEvents ID when structured CloudEvents are enabled.
- External plugin servers should be stateless or persist enough backend state to
  answer repeated Kapro requests after restart.
- Timeout errors should be safe to retry. If a backend operation may still be in
  flight after timeout, the next request must observe backend state before
  issuing another write.
- Admission and automation should avoid mutating in-flight Releases except for
  documented approval, cancellation, or rollback workflows.
