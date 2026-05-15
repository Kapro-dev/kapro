# Operations

This guide documents the operational posture for running Kapro as a fleet
promotion controller.

## Metrics Endpoint

The operator exposes Prometheus metrics on `:8080` by default. The Helm chart
and kustomize manifests should expose this port as `metrics`.

Kapro-specific metrics use the `kapro_` namespace:

| Metric | Type | Use |
|---|---|---|
| `kapro_controller_reconciles_total` | counter | Reconcile volume and error rate by controller |
| `kapro_controller_reconcile_duration_seconds` | histogram | Controller reconcile latency |
| `kapro_controller_status_writes_total` | counter | Status write success and failure rate |
| `kapro_sync_transitions_total` | counter | Target FSM phase transitions |
| `kapro_gate_evaluations_total` | counter | Gate pass, fail, inconclusive, and error rate |
| `kapro_stage_duration_seconds` | histogram | Stage duration by pipeline |
| `kapro_release_active_total` | gauge | Non-terminal Releases |
| `kapro_wave_environments_promoted_total` | gauge | Promoted targets by release and stage |
| `kapro_plugin_probe_results_total` | counter | Plugin probe success and failure rate |
| `kapro_plugin_probe_duration_seconds` | histogram | Plugin probe latency |
| `kapro_plugin_probe_ready` | gauge | Latest plugin readiness by type and name |
| `kapro_plugin_runtime_calls_total` | counter | Runtime plugin call result counts |
| `kapro_plugin_runtime_call_duration_seconds` | histogram | Runtime plugin latency |
| `kapro_plugin_runtime_registered` | gauge | Startup-time registered plugin adapters |

Controller-runtime and Go runtime metrics are also exposed from the same
endpoint.

## Dashboard and Alerts

Example assets are provided under `monitoring/`:

- `monitoring/grafana/kapro-operations-dashboard.json`
- `monitoring/prometheus/kapro-alerts.yaml`

Installable examples for Prometheus Operator and kube-state-metrics live under
`examples/monitoring/`:

- `examples/monitoring/prometheus-rules.yaml`
- `examples/monitoring/grafana-dashboard.json`
- `examples/monitoring/kube-state-metrics-crd-config.yaml`

See `docs/monitoring.md` for the metric inventory and installation notes.

The dashboard covers:

- release backlog and active Releases;
- release stuck symptoms through controller error rate and active backlog;
- gate failure rate;
- plugin probe failures and readiness;
- trigger blocked symptoms through ReleaseTrigger reconcile errors;
- rollout duration p95 via stage duration histogram.

The alert rules cover:

| Alert | Signal |
|---|---|
| `KaproReleaseStuck` | Active Releases remain non-terminal for a sustained window |
| `KaproGateFailureRateHigh` | Gate failures/errors exceed 10% of evaluations |
| `KaproPluginProbeFailures` | Plugin probe failures or plugin readiness drops |
| `KaproReleaseTriggerBlocked` | ReleaseTrigger reconciles are failing |
| `KaproRolloutDurationP95High` | Stage duration p95 exceeds the configured threshold |
| `KaproControllerReconcileErrors` | Any controller has sustained reconcile errors |

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

## Sharding

Set `KAPRO_SHARD` on an operator replica set to enable shard selection. The
controller logs the shard name at startup and uses shard predicates from
`internal/shard`.

Recommended model:

- Run one shard per major environment or region.
- Assign objects using a stable shard label such as `kapro.io/shard`.
- Keep one unsharded controller only in small development clusters.
- Do not run overlapping shards against the same object set unless leader
  election and selectors make ownership unambiguous.

## Large Fleet Assumptions

Kapro is designed for hub-and-spoke promotion where the hub stores desired
promotion state and spoke controllers or GitOps backends converge local
workloads.

Current practical assumptions:

- Kubernetes API is the source of truth for release state.
- Plugins are idempotent and bounded by request context.
- One target rollout is represented by one `ReleaseTarget`.
- Stage fan-out is controlled by pipeline strategy, not by unbounded goroutines.
- Status updates are small and append bounded summaries rather than complete
  historical logs.

For fleets above roughly 1,000 targets per hub, use sharding, conservative stage
`maxParallel`, and external long-term event storage. Kapro Events and status are
operational state, not an infinite audit warehouse.

## First Response

Use the same first checks for every incident:

```bash
kubectl get releases,releasetargets,releasetriggers,pluginregistrations
kubectl describe release <release>
kubectl get releasetargets -l kapro.io/release=<release> -o wide
kubectl get events --field-selector involvedObject.name=<release> --sort-by=.lastTimestamp
kubectl logs -n kapro-system deploy/kapro-operator --since=30m
```

If the deployment uses sharding, include the shard label in every query:

```bash
kubectl get releases,releasetargets -l kapro.io/shard=<shard>
```

For dashboard triage, start with active release count, controller reconcile
errors, status write errors, gate failure ratio, plugin readiness, and blocked
ReleaseTrigger panels.

## Runbook: Stuck Release

Symptoms:

- `Release.status.phase` remains `Pending` or `Progressing` past the expected
  rollout window or `spec.timeout`.
- `KaproReleaseStuck` fires.
- `kapro_release_active_total` remains non-zero while no target appears to move.

Triage:

1. Inspect the top-level summary:

   ```bash
   kubectl get release <release> -o yaml
   kubectl describe release <release>
   ```

   Check `status.pipelineProgress`, `status.report`, `status.conditions`, and
   `spec.suspended`.

2. Inspect child execution objects:

   ```bash
   kubectl get releasetargets -l kapro.io/release=<release> -o wide
   kubectl get releasetargets -l kapro.io/release=<release> -o yaml
   ```

   The phase that matters is `ReleaseTarget.status.phase`: `Verification`,
   `HealthCheck`, `Soaking`, `MetricsCheck`, `WaitingApproval`, `Applying`,
   `Converged`, `Failed`, or `Skipped`.

3. Map the phase to the likely blocker:

| Phase | Likely blocker | Next check |
|---|---|---|
| `Pending` | Stage dependency, planner deferral, missing target selection, suspended Release | `status.pipelineProgress[].stageProgress[].plannerResults`, `spec.suspended` |
| `Verification` | Artifact verification failure or retry | ReleaseTarget Events and verification gate message |
| `HealthCheck` | `MemberCluster.status.health` not ready or heartbeat stale | `kubectl get membercluster <target> -o yaml` |
| `Soaking` | Normal soak delay | `status.phaseEnteredAt` and configured soak duration |
| `MetricsCheck` | Prometheus query false, inconclusive, or unreachable | Gate results, `kapro_gate_evaluations_total`, Prometheus target health |
| `WaitingApproval` | Approval not created or rejected | `kubectl get approvals` and approval webhook logs |
| `Applying` | Actuator backend not converging | `MemberCluster.status.currentVersions`, actuator/plugin logs |
| `Failed` | Failure policy halted or rollback failed | ReleaseTarget message and Events |

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
  kubectl patch release <release> --type=merge -p '{"spec":{"suspended":false}}'
  ```

- If one target is blocked by a known transient backend issue, fix the backend
  and let the ReleaseTarget reconcile. Avoid patching status by hand.
- If a stage is too wide for the backend, suspend the Release, reduce future
  `Stage.spec.strategy.maxParallel`, and let the current target set drain or
  fail according to policy.
- If the release is failed and the artifact should not continue, use the
  rollback runbook below.

## Runbook: Gate Failure

Symptoms:

- `KaproGateFailureRateHigh` fires.
- A ReleaseTarget is in `MetricsCheck`, `Verification`, or `Failed`.
- `status.gates[]` shows `Failed`, `Running`, or repeated `Inconclusive`.

Triage:

1. Identify the target and gate:

   ```bash
   kubectl get releasetargets -l kapro.io/release=<release> -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\t"}{.status.message}{"\n"}{end}'
   kubectl describe releasetarget <release-target>
   ```

2. Check the gate policy snapshot on the ReleaseTarget, not only the current
   Pipeline template:

   ```bash
   kubectl get releasetarget <release-target> -o jsonpath='{.spec.gate}{"\n"}'
   kubectl get releasetarget <release-target> -o jsonpath='{.status.gates}{"\n"}'
   ```

3. For metrics gates, run the recorded PromQL query directly against the same
   Prometheus endpoint configured in the gate. Confirm the query returns a
   non-zero pass value for the same window.

4. For template gates, check `status.gates[].attempts`,
   `failurePolicy`, `inconclusivePolicy`, `timeout`, and backend logs for job,
   webhook, or plugin implementations.

Mitigation:

- Fix the underlying service or telemetry query and let the next reconcile
  re-evaluate the gate.
- If the gate policy is wrong, create a new Release or Pipeline revision for the
  corrected policy. Existing ReleaseTargets keep a snapshot of the gate policy
  they were created with.
- If failure is expected and the policy allows it, confirm whether
  `onFailure=continue` or stage `onFailure=skip` is the intended behavior for
  future releases.
- For external gate plugins, use the plugin-not-ready runbook if the
  PluginRegistration is not ready.

## Runbook: Blocked ReleaseTrigger

Symptoms:

- `KaproReleaseTriggerBlocked` fires.
- `ReleaseTrigger.status.conditions` includes `Stalled=True`,
  `ReleaseCreated=False`, or reasons such as `CooldownActive`,
  `MaxActiveReached`, `ResolveFailed`, `SignatureVerificationFailed`,
  `VerifierUnavailable`, or `ReleaseCreateFailed`.
- No new Release appears for a tag that should match.

Triage:

```bash
kubectl get releasetrigger <trigger> -o yaml
kubectl describe releasetrigger <trigger>
kubectl get releases -l kapro.io/release-trigger=<trigger>
```

Check these fields in order:

| Field | Meaning | Action |
|---|---|---|
| `spec.suspended` | Source observation is paused | Resume only if automation is intended |
| `spec.dryRun` | Controller records what it would create | Disable dry run to create Releases |
| `spec.source.oci.tagPattern` | Tags outside the regex are ignored | Test the regex against the pushed tag |
| `spec.source.oci.requireSignature` | Signature verification must pass | Verify signer, keyless identity, or verifier availability |
| `spec.cooldown` | Minimum interval between created Releases | Wait or adjust future trigger policy |
| `spec.maxActive` | Active Releases created by the trigger are capped | Complete, fail, or suspend existing active Releases before expecting another |
| `status.lastArtifact` | Last observed tag, digest, and verification result | Confirm the digest is the expected immutable artifact |

Mitigation:

- Do not bypass the trigger by creating an unsuspended production Release unless
  incident command explicitly accepts that risk.
- If `MaxActiveReached`, inspect the active Releases and resolve the oldest
  non-terminal one first.
- If signature verification failed, fix the artifact or verifier; do not lower
  signature policy for production as a quick workaround.
- If the trigger created a suspended Release as designed, review it and then
  unsuspend the Release, not the trigger policy:

  ```bash
  kubectl patch release <release> --type=merge -p '{"spec":{"suspended":false}}'
  ```

## Runbook: Plugin Not Ready

Symptoms:

- `KaproPluginProbeFailure` or `KaproPluginProbeFailures` fires.
- `PluginRegistration.status.ready=false`.
- Runtime plugin calls fail or no plugin adapters register at operator startup.

Triage:

```bash
kubectl get pluginregistrations
kubectl describe pluginregistration <pluginregistration>
kubectl get pluginregistration <pluginregistration> -o yaml
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

Runtime registration is startup-time only for actuator and gate plugins when
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`. If a PluginRegistration becomes ready after
the operator starts, restart or roll the operator after confirming readiness so
the runtime adapter is loaded.

Mitigation:

- Restore the plugin Service, DNS, TLS Secret, or backend dependency.
- Increase `spec.timeout` only when the plugin is healthy but its normal call
  latency exceeds the current deadline.
- For planner plugins, readiness is reported, but runtime planner dispatch is
  not wired into release execution yet.
- If the plugin is optional, remove or change future Pipeline gate templates or
  MemberCluster actuator references that require it. Existing in-flight
  ReleaseTargets should be allowed to reconcile or fail according to policy.

## Runbook: Rollback

Rollback is a delivery action, not a status edit. Do not patch
`Release.status` or `ReleaseTarget.status` to force rollback.

Automatic rollback path:

1. Confirm the pipeline or gate policy is configured for rollback:

   ```bash
   kubectl get pipeline <pipeline> -o yaml
   kubectl get releasetarget <release-target> -o yaml
   ```

   Gate rollback uses `spec.gate.onFailure=rollback` on the ReleaseTarget
   snapshot. Stage rollback uses `Stage.onFailure=rollback` in the Pipeline.

2. Confirm the failed target has a previous version:

   ```bash
   kubectl get releasetarget <release-target> -o jsonpath='{.status.previousVersion}{"\n"}'
   kubectl get releasetarget <release-target> -o jsonpath='{.status.previousVersions}{"\n"}'
   ```

3. Watch for a rollback ReleaseTarget:

   ```bash
   kubectl get releasetargets -l kapro.io/release=<release> -o wide
   kubectl get events --field-selector reason=RollbackTriggered --sort-by=.lastTimestamp
   ```

   Rollback targets have `spec.rollback=true` and should progress through the
   normal target FSM.

Manual corrective rollback path:

1. Identify the last known good digest from `ReleaseTarget.status.previousVersion`,
   `previousVersions`, artifact inventory, or backend deployment records.
2. Create a new Release pinned to that immutable digest and scoped to the
   affected targets.
3. Use conservative stage `maxParallel` and approval gates for production
   targets.
4. Keep the failed Release for audit. Do not delete it until incident review
   confirms that Events, status, and external audit sinks captured the failure.

Post-rollback checks:

```bash
kubectl get releases,releasetargets
kubectl get memberclusters -o yaml
```

Confirm target `currentVersions` match the rollback version and that downstream
GitOps or delivery backends report healthy convergence.
