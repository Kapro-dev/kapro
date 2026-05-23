# Operations

This guide documents the operational posture for running Kapro as a fleet
promotion controller.

## Metrics Endpoint

The operator exposes Prometheus metrics on `:8080` by default. The Helm chart
and kustomize manifests should expose this port as `metrics`.

`kapro-cluster-controller` exposes a separate spoke-side `/metrics` endpoint
using `KAPRO_METRICS_ADDR` (`:8080` by default, `off` to disable). The
cluster-controller Helm chart creates a metrics Service when
`metrics.enabled=true`.

Operator metrics use the `kapro_` namespace:

| Metric | Type | Use |
|---|---|---|
| `kapro_controller_reconciles_total` | counter | Reconcile volume and error rate by controller |
| `kapro_controller_reconcile_duration_seconds` | histogram | Controller reconcile latency |
| `kapro_controller_status_writes_total` | counter | Status write success and failure rate |
| `kapro_sync_transitions_total` | counter | Target FSM phase transitions |
| `kapro_gate_evaluations_total` | counter | Gate pass, fail, inconclusive, and error rate |
| `kapro_stage_duration_seconds` | histogram | Stage duration by plan |
| `kapro_promotionrun_active_total` | gauge | Non-terminal PromotionRuns |
| `kapro_wave_environments_promoted_total` | gauge | Promoted targets by promotionrun and stage |
| `kapro_plugin_probe_results_total` | counter | Plugin probe success and failure rate |
| `kapro_plugin_probe_duration_seconds` | histogram | Plugin probe latency |
| `kapro_plugin_probe_ready` | gauge | Latest plugin readiness by type and name |
| `kapro_plugin_runtime_calls_total` | counter | Runtime plugin call result counts |
| `kapro_plugin_runtime_call_duration_seconds` | histogram | Runtime plugin latency |
| `kapro_plugin_runtime_registered` | gauge | Startup-time registered plugin adapters |
| `kapro_fleetdriftreport_targets` | gauge | FleetDriftReport target counts by report and state |
| `kapro_fleetdriftreport_backend_objects` | gauge | FleetDriftReport backend object counts by report and state |
| `kapro_fleetdriftreport_phase` | gauge | One-hot FleetDriftReport phase by report |

Controller-runtime and Go runtime metrics are also exposed from the same
endpoint.

Spoke metrics use the same namespace but are emitted by each
`kapro-cluster-controller` pod:

| Metric | Type | Use |
|---|---|---|
| `kapro_spoke_delivery_reconciles_total` | counter | Spoke delivery outcomes by cluster, backend, phase, and result |
| `kapro_spoke_delivery_reconcile_duration_seconds` | histogram | Spoke delivery reconcile duration |

## Dashboard and Alerts

Generic assets are provided under `examples/monitoring/`:

- `examples/monitoring/kapro-operations-dashboard.json`
- `examples/monitoring/kapro-alerts.yaml`

Installable examples for Prometheus Operator and kube-state-metrics live under
`examples/monitoring/`:

- `examples/monitoring/prometheus-rules.yaml`
- `examples/monitoring/grafana-dashboard.json`
- `examples/monitoring/kube-state-metrics-crd-config.yaml`

See `docs/monitoring.md` for the metric inventory and installation notes.
See `docs/operator-slos.md` for recommended SLI queries, thresholds, and known
first-class metric gaps.

The dashboard covers:

- promotionrun backlog and active PromotionRuns;
- promotionrun stuck symptoms through controller error rate and active backlog;
- gate failure rate;
- plugin probe failures and readiness;
- trigger blocked symptoms through Trigger reconcile errors;
- rollout duration p95 via stage duration histogram.
- FleetDriftReport phase and drift count panels.

The alert rules cover:

| Alert | Signal |
|---|---|
| `KaproPromotionRunStuck` | Active PromotionRuns remain non-terminal for a sustained window |
| `KaproGateFailureRateHigh` | Gate failures/errors exceed 20% of evaluations |
| `KaproPluginProbeFailures` | Plugin probe failures or plugin readiness drops |
| `KaproTriggerBlocked` | Trigger reconciles are failing |
| `KaproRolloutDurationP95High` | Stage duration p95 exceeds the configured threshold |
| `KaproLifecycleSinkP99High` | CloudEvents sink dispatch p99 exceeds the configured threshold |
| `KaproControllerReconcileErrors` | Any controller has sustained reconcile errors |
| `KaproFleetDriftDetected` | A FleetDriftReport stays `Drifted` beyond the allowed window |
| `KaproFleetDriftSignalsIncomplete` | A FleetDriftReport stays `Unknown` because cluster/version signals are missing |
| `KaproFleetDriftReportFailed` | A FleetDriftReport stays `Failed` |
| `KaproFleetDriftReportPending` | A FleetDriftReport stays `Pending` beyond the rollout window |
| `KaproSpokeDeliveryErrors` | A spoke reports sustained delivery reconcile errors |
| `KaproSpokeDeliveryLatencyHigh` | Spoke delivery p95 exceeds the configured threshold |

Tune alert windows and thresholds per fleet size. Small test clusters should use
longer `for` windows to avoid noise from deliberate failure tests.

## Rate Limits and Workqueue Tuning

Kapro uses controller-runtime workqueues and Kubernetes API backoff. The
operator currently sets manager-wide `MaxConcurrentReconciles` to `5`.

Operational guidance:

- Start with `5` concurrent reconciles for hub clusters below 500 targets.
- Raise concurrency only after watching API server throttling and status write
  errors.
- Keep plugin timeouts short. A slow plugin call occupies reconcile capacity.
- Prefer gate `interval` values of at least `30s`; the runtime clamps invalid or
  too-small metric intervals to safe defaults.
- Use controller sharding before pushing a single manager beyond the Kubernetes
  API server's comfortable QPS budget.

Tune rollout inputs before changing worker counts:

- Use stage `strategy.maxParallel` to bound backend write pressure.
- Prefer more stages over one wide stage when backend APIs have tenant or
  region quotas.
- Keep actuator `Apply` idempotent and cheap when the desired version is already
  present.
- Return longer gate `retryAfter` values for slow external checks.
- Create objects with final labels, plan references, and shard labels already
  set so duplicate queue events are minimized.

Signals that a queue needs partitioning or tuning:

- controller workqueue depth grows while reconcile errors stay low;
- `kapro_controller_reconcile_duration_seconds` rises with fleet size;
- `kapro_controller_status_writes_total{result="error"}` rises during large
  stages;
- plugin RPC latency approaches `Plugin.spec.timeout`;
- many gates remain `Running` or `Inconclusive` at the same time.

## Sharding

Set `KAPRO_SHARD` on an operator replica set to enable shard selection. The
controller logs the shard name at startup and uses shard predicates from
`internal/shard`.

Recommended model:

- Run one shard per major environment or region.
- Assign objects using a stable shard label such as `kapro.io/shard`.
- Set `KAPRO_SHARD_DEFAULT=true` on exactly one shard if unlabeled objects
  should still be processed.
- Use the default shard-specific leader election ID, or set
  `KAPRO_LEADER_ELECTION_ID` explicitly for each shard deployment.
- Keep one unsharded controller only in small development clusters.
- Do not run overlapping shards against the same object set unless leader
  election and selectors make ownership unambiguous.

## Large Fleet Assumptions

Kapro is designed for hub-and-spoke promotion where the hub stores desired
promotion state and spoke controllers or GitOps backends converge local
workloads.

For multi-cloud and air-gapped fleets, prefer
`Cluster.spec.delivery.mode: pull` with a `Backend` selected by
`spec.delivery.backendRef`. In pull mode the hub writes desired versions to
`Cluster.spec` and does not patch spoke workloads directly during a
promotionrun. Each spoke applies the desired state locally through its selected
backend, reports `status.currentVersions` and `status.health`, and renews
`Lease/kapro-heartbeat-<cluster>` in the operator namespace. The Target
controller defers pull-mode targets while that heartbeat is stale. A
PromotionRun can still fail on its global timeout while individual Targets
remain deferred by heartbeat state.

Current practical assumptions:

- Kubernetes API is the source of truth for promotionrun state.
- Plugins are idempotent and bounded by request context.
- One target rollout is represented by one `Target`.
- Stage fan-out is controlled by plan strategy, not by unbounded goroutines.
- Status updates are small and append bounded summaries rather than complete
  historical logs.

For fleets above roughly 1,000 targets per hub, use sharding, conservative stage
`maxParallel`, and external long-term event storage. Kapro Events and status are
operational state, not an infinite audit warehouse.

## CLI Observability

Use the Kapro CLI for the first operational read before dropping to raw
`kubectl` YAML:

```bash
kapro doctor
kapro top
kapro get promotion checkout
kapro tree checkout
kapro events --promotion checkout --since=30m
kapro diag checkout
```

`kapro doctor` is the read-only preflight for hub installation health: CRD
establishment, operator readiness, operator RBAC, webhook wiring, and referenced
pull secrets. Exit code is `0` when all required checks pass and `1` when any
required check fails; advisory WARN/SKIP findings do not fail the command.
`kapro top` renders Promotion intent rows with active-attempt target counts.
Use `kapro top --watch --watch-interval=2s` during a rollout. JSON output is
one-shot only. `kapro tree` shows the runtime hierarchy from Promotion to
PromotionRun attempts to Target children. `kapro get promotion` summarizes the
active attempt, lifecycle handler outcomes, recent Events, and current or most
recent Target state. `kapro events` reads Kubernetes Events for Kapro API
objects; it is the CLI fallback view when an operator CloudEvents sink is not
exposed directly to the user.

## First Response

Use the same first checks for every incident:

```bash
kapro doctor
kapro top
kapro tree <promotion>
kapro events --promotion <promotion> --since=30m
kapro why <promotionrun>
kubectl get promotionruns,targets,triggers,plugins
kubectl describe promotionrun <promotionrun>
kubectl get targets -l kapro.io/promotionrun=<promotionrun> -o wide
kubectl get decisiontraces -l kapro.io/promotionrun=<promotionrun>
kubectl get events --field-selector involvedObject.name=<promotionrun> --sort-by=.lastTimestamp
kubectl logs -n kapro-system deploy/kapro-operator --since=30m
```

If the deployment uses sharding, include the shard label in every query:

```bash
kubectl get promotionruns,targets -l kapro.io/shard=<shard>
```

For dashboard triage, start with active promotionrun count, controller reconcile
errors, status write errors, gate failure ratio, plugin readiness, and blocked
Trigger panels.

## Runbook: Stuck PromotionRun

Symptoms:

- `PromotionRun.status.phase` remains `Pending` or `Progressing` past the expected
  rollout window or `spec.timeout`.
- `KaproPromotionRunStuck` fires.
- `kapro_promotionrun_active_total` remains non-zero while no target appears to move.

Triage:

1. Inspect the top-level summary:

   ```bash
   kubectl get promotionrun <promotionrun> -o yaml
   kubectl describe promotionrun <promotionrun>
   ```

   Check `status.summary`, `status.planProgress`, `status.report`,
   `status.conditions`, and `spec.suspended`.

2. Inspect child execution objects:

   ```bash
   kapro why <promotionrun>
   kubectl get targets -l kapro.io/promotionrun=<promotionrun> -o wide
   kubectl get targets -l kapro.io/promotionrun=<promotionrun> -o yaml
   ```

   The phase that matters is `Target.status.phase`: `Verification`,
   `HealthCheck`, `Soaking`, `MetricsCheck`, `WaitingApproval`, `Applying`,
   `Converged`, `Failed`, or `Skipped`.

3. Map the phase to the likely blocker:

| Phase | Likely blocker | Next check |
|---|---|---|
| `Pending` | Stage dependency, planner deferral, missing target selection, suspended PromotionRun | `status.planProgress[].stageProgress[].plannerResults`, `spec.suspended` |
| `Verification` | Artifact verification failure or retry | Target Events and verification gate message |
| `HealthCheck` | `Cluster.status.health` not ready or heartbeat stale | `kubectl get cluster <target> -o yaml` |
| `Soaking` | Normal soak delay | `status.phaseEnteredAt` and configured soak duration |
| `MetricsCheck` | Prometheus query false, inconclusive, or unreachable | Gate results, `kapro_gate_evaluations_total`, Prometheus target health |
| `WaitingApproval` | Approval not created or rejected | `kubectl get approvals` and approval webhook logs |
| `Applying` | Actuator backend not converging | `Cluster.status.currentVersions`, actuator/plugin logs |
| `Failed` | Failure policy halted or rollback failed | Target message and Events |

4. Check whether this is isolated or systemic:

   ```promql
   sum by (controller, result) (rate(kapro_controller_reconciles_total[10m]))
   sum by (resource, result) (rate(kapro_controller_status_writes_total[10m]))
   sum by (gate_type, result) (rate(kapro_gate_evaluations_total[10m]))
   ```

Mitigation:

- If `spec.suspended=true`, resume only after confirming the intended artifact
  and scope:

  ```bash
  kubectl patch promotionrun <promotionrun> --type=merge -p '{"spec":{"suspended":false}}'
  ```

- If one target is blocked by a known transient backend issue, fix the backend
  and let the Target reconcile. Avoid patching status by hand.
- If a stage is too wide for the backend, suspend the PromotionRun, reduce future
  stage `strategy.maxParallel`, and let the current target set drain or
  fail according to policy.
- If the promotionrun is failed and the artifact should not continue, use the
  rollback runbook below.

## Runbook: Fleet Drift

Symptoms:

- `KaproFleetDriftDetected`, `KaproFleetDriftSignalsIncomplete`,
  `KaproFleetDriftReportFailed`, or `KaproFleetDriftReportPending` fires.
- `kapro_fleetdriftreport_phase{phase!="Current"} == 1` returns an active
  non-current phase.
- A `max-drift` gate is repeatedly `Inconclusive`.

Triage:

1. Inspect the report summary and bounded evidence:

   ```bash
   kubectl get fleetdriftreport <report> -o yaml
   kubectl describe fleetdriftreport <report>
   ```

   Start with `status.phase`, `status.summary`, and `status.targets[]`.
   Current targets are intentionally omitted from `status.targets`; use summary
   counts to understand fleet-wide impact.

2. Map the report phase to the likely source:

| Phase | Likely source | Next check |
|---|---|---|
| `Drifted` | Converged target or backend-native object differs from desired state | `status.targets[].appVersions`, `status.targets[].objects`, backend controller status |
| `Unknown` | Missing Cluster object or incomplete version signal | `kubectl get cluster <cluster> -o yaml`, spoke heartbeat and delivery status |
| `Failed` | Target or delivery loop reports failure | Target status, `Cluster.status.delivery`, backend/plugin logs |
| `Pending` | Rollout is still converging | PromotionRun/Target progress and stage maxParallel |

3. Check whether the problem is one target or the full slice:

   ```promql
   kapro_fleetdriftreport_targets{report="<report>"}
   kapro_fleetdriftreport_backend_objects{report="<report>"}
   kapro_fleetdriftreport_phase{report="<report>"} == 1
   ```

Mitigation:

- For `Drifted`, fix the backend source of truth or allow the backend to
  reconcile; do not patch report status.
- For `Unknown`, restore the missing Cluster/status signal before relaxing a
  `max-drift` gate.
- For `Failed`, follow the Target or backend failure first, then let the report
  refresh.
- Use `allowMissing=true` or `allowStale=true` on `max-drift` only for
  deliberate bootstrap windows; remove it after the report controller is healthy.

## Runbook: Spoke Delivery

Symptoms:

- `KaproSpokeDeliveryErrors` or `KaproSpokeDeliveryLatencyHigh` fires.
- `kapro_spoke_delivery_reconciles_total{result="error"}` increases.
- `Cluster.status.delivery[app].lastError` is populated or stale.

Triage:

1. Inspect the hub-side Cluster delivery status:

   ```bash
   kubectl get cluster <cluster> -o yaml
   ```

   Check `status.delivery`, `status.currentVersions`, heartbeat freshness, and
   whether `spec.suspend=true`.

2. Inspect the spoke pod and local backend:

   ```bash
   kubectl -n kapro-system logs -l app.kubernetes.io/component=spoke-agent --since=30m
   kubectl -n kapro-system get svc -l app.kubernetes.io/component=spoke-agent
   ```

3. Check whether errors are backend-specific:

   ```promql
   sum by (cluster, backend, result) (rate(kapro_spoke_delivery_reconciles_total[10m]))
   histogram_quantile(0.95, sum by (cluster, backend, le) (rate(kapro_spoke_delivery_reconcile_duration_seconds_bucket[10m])))
   ```

Mitigation:

- For `backend="oci"`, verify artifact pull credentials, artifact existence,
  and server-side apply conflicts in the spoke logs.
- For `backend="flux"`, inspect local `OCIRepository` and `HelmRelease`
  readiness.
- If latency rises without errors, check spoke API server throttling and plugin
  or backend controller response time before increasing delivery interval.

## Runbook: Gate Failure

Symptoms:

- `KaproGateFailureRateHigh` fires.
- A Target is in `MetricsCheck`, `Verification`, or `Failed`.
- `Target.status.gates[]` shows `Failed`, `Running`, or repeated `Inconclusive`.

Triage:

1. Identify the target and gate:

   ```bash
   kubectl get targets -l kapro.io/promotionrun=<promotionrun> -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\t"}{.status.message}{"\n"}{end}'
   kubectl describe target <target>
   ```

2. Check the gate policy snapshot on the Target, not only the current
   Plan template:

   ```bash
   kubectl get target <target> -o jsonpath='{.spec.gate}{"\n"}'
   kubectl get target <target> -o jsonpath='{.status.gates}{"\n"}'
   ```

3. For metrics gates, run the recorded PromQL query directly against the same
   Prometheus endpoint configured in the gate. Confirm the query returns a
   non-zero pass value for the same window.

4. For template gates, check `Target.status.gates[].attempts`,
   `failurePolicy`, `inconclusivePolicy`, `timeout`, and backend logs for job,
   webhook, or plugin implementations.

Mitigation:

- Fix the underlying service or telemetry query and let the next reconcile
  re-evaluate the gate.
- If the gate policy is wrong, update the Promotion or create a Plan
  revision for the corrected policy. Existing Targets keep a snapshot
  of the gate policy they were created with.
- If failure is expected and the policy allows it, confirm whether
  `onFailure=continue` or stage `onFailure=skip` is the intended behavior for
  future promotionruns.
- For external gate plugins, use the plugin-not-ready runbook if the
  Plugin is not ready.

## Runbook: Blocked Trigger

Symptoms:

- `KaproTriggerBlocked` fires.
- `Trigger.status.conditions` includes `Stalled=True`,
  `PromotionUpdated=False`, or reasons such as `CooldownActive`,
  `MaxActiveReached`, `ResolveFailed`, `SignatureVerificationFailed`,
  `VerifierUnavailable`, `PromotionCreateFailed`, or
  `PromotionUpdateFailed`.
- The managed Promotion does not update for a tag that should match.

Triage:

```bash
kubectl get trigger <trigger> -o yaml
kubectl describe trigger <trigger>
kubectl get promotion "$(kubectl get trigger <trigger> -o jsonpath='{.status.managedPromotion}')"
kubectl get promotionruns -l kapro.io/promotion="$(kubectl get trigger <trigger> -o jsonpath='{.status.managedPromotion}')"
```

Check these fields in order:

| Field | Meaning | Action |
|---|---|---|
| `spec.suspended` | Source observation is paused | Resume only if automation is intended |
| `spec.dryRun` | Controller records what it would create | Disable dry run to create or update Promotions |
| `spec.source.oci.tagPattern` | Tags outside the regex are ignored | Test the regex against the pushed tag |
| `spec.source.oci.requireSignature` | Signature verification must pass | Verify signer, keyless identity, or verifier availability |
| `spec.cooldown` | Minimum interval between Promotion updates | Wait or adjust future trigger policy |
| `spec.maxActive` | Active attempts for the managed Promotion are capped | Complete, fail, or suspend existing active PromotionRuns before expecting another |
| `status.lastArtifact` | Last observed tag, digest, and verification result | Confirm the digest is the expected immutable artifact |

Mitigation:

- Do not bypass the trigger by creating an unsuspended production Promotion unless
  incident command explicitly accepts that risk.
- If `MaxActiveReached`, inspect the active PromotionRuns and resolve the oldest
  non-terminal one first.
- If signature verification failed, fix the artifact or verifier; do not lower
  signature policy for production as a quick workaround.
- If the trigger created or updated a suspended Promotion as designed, review it
  and then unsuspend the Promotion, not the trigger policy:

  ```bash
  kubectl patch promotion <promotion> --type=merge -p '{"spec":{"suspended":false}}'
  ```

## Runbook: Plugin Not Ready

Symptoms:

- `KaproPluginProbeFailure` or `KaproPluginProbeFailures` fires.
- `Plugin.status.ready=false`.
- Runtime plugin calls fail or no plugin adapters register at operator startup.

Triage:

```bash
kubectl get plugins
kubectl describe plugin <plugin>
kubectl get plugin <plugin> -o yaml
kubectl logs -n kapro-system deploy/kapro-operator --since=30m
```

Check:

- `status.observedGeneration == metadata.generation`;
- `status.conditions[type=Ready]`;
- `status.conditions[type=Compatible]`;
- `status.contractVersion`;
- `spec.endpoint`;
- `spec.timeout`;
- referenced TLS Secret namespace and name;
- plugin pod/service readiness in the plugin namespace.

Runtime dispatch is hot-loaded for actuator, gate, and planner plugins when
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`. If a `Plugin` becomes ready
after the operator starts, changes generation, becomes incompatible, or is
deleted, the operator refreshes the runtime adapter without requiring a restart.

Mitigation:

- Restore the plugin Service, DNS, TLS Secret, or backend dependency.
- Increase `spec.timeout` only when the plugin is healthy but its normal call
  latency exceeds the current deadline.
- If the plugin is optional, remove or change future Plan gate templates or
  Cluster actuator references that require it. Existing in-flight
  Targets should be allowed to reconcile or fail according to policy.

## Runbook: Rollback

Rollback is a delivery action, not a status edit. Do not patch
`PromotionRun.status` or `Target.status` to force rollback.

Automatic rollback path:

1. Confirm the plan or gate policy is configured for rollback:

   ```bash
   kubectl get plan <plan> -o yaml
   kubectl get target <target> -o yaml
   ```

   Gate rollback uses `spec.gate.onFailure=rollback` on the Target
   snapshot. Stage rollback uses `Stage.onFailure=rollback` in the Plan.

2. Confirm the failed target has a previous version:

   ```bash
   kubectl get target <target> -o jsonpath='{.status.previousVersion}{"\n"}'
   kubectl get target <target> -o jsonpath='{.status.previousVersions}{"\n"}'
   ```

3. Watch for a rollback Target:

   ```bash
   kubectl get targets -l kapro.io/promotionrun=<promotionrun> -o wide
   kubectl get events --field-selector reason=RollbackTriggered --sort-by=.lastTimestamp
   ```

   Rollback targets have `spec.rollback=true` and should progress through the
   normal target FSM.

Manual corrective rollback path:

1. Identify the last known good digest from `Target.status.previousVersion`,
   `previousVersions`, artifact inventory, or backend deployment records.
2. Create or update a Promotion pinned to that immutable digest and scoped to
   the affected targets. Let the controller stamp the rollback attempt.
3. Use conservative stage `maxParallel` and approval gates for production
   targets.
4. Keep the failed PromotionRun for audit. Do not delete it until incident review
   confirms that Events, status, and external audit sinks captured the failure.

Post-rollback checks:

```bash
kubectl get promotionruns,targets
kubectl get clusters -o yaml
```

Confirm target `currentVersions` match the rollback version and that downstream
GitOps or delivery backends report healthy convergence.
