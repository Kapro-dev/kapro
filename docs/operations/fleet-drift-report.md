# Fleet Drift Reports

`FleetDriftReport` is a preview, read-only report for desired-vs-observed
delivery state across Kapro targets.

It does not drive promotion, mutate clusters, or poll external substrates
directly. The controller reads existing Kapro status:

- `Target.spec` and `Target.status` for desired versions, rollout phase, and
  backend-native object evidence;
- `Cluster.status.currentVersions`, `status.version`, and `status.delivery`
  for observed spoke state;
- `PromotionRun` labels for optional `fleetRef` scoping.

## Enable

The CRD is installed with the operator chart, but the controller is opt-in:

```yaml
controllers:
  - fleet
  - plan
  - promotion
  - promotionrun
  - cluster
  - fleetdriftreport
```

The compatibility alias `fleet-drift-report` is also accepted.

## Example

```yaml
apiVersion: kapro.io/v1alpha2
kind: FleetDriftReport
metadata:
  name: checkout-prod
spec:
  fleetRef: checkout
  targetSelector:
    matchLabels:
      tier: prod
  maxTargets: 128
  syncInterval: 5m
```

The report writes bounded evidence to `status.targets` only for non-current
targets. Current targets are counted in `status.summary` but are not repeated
in full, keeping the status object useful at fleet scale.

## Status Semantics

`status.phase` is derived from observed targets:

| Phase | Meaning |
|---|---|
| `Current` | Every included target matches its desired version. |
| `Pending` | One or more targets are still converging to desired state. |
| `Drifted` | A converged target or backend object differs from desired state. |
| `Unknown` | Required cluster or version signals are missing. |
| `Failed` | A target or cluster delivery loop reports failure. |

The controller also sets the standard `Ready`, `Reconciling`, and `Stalled`
conditions. `Drifted`, `Unknown`, and `Failed` set `Ready=False` and
`Stalled=True`; `Pending` sets `Reconciling=True`.

## Scope

Reports may be narrowed with:

- `spec.fleetRef` for PromotionRuns created for one Fleet;
- `spec.promotionRunRef` for one execution attempt;
- `spec.targetSelector` for labels on `Target` objects.

When both `fleetRef` and `promotionRunRef` are set, both must match.

## Ownership

`FleetDriftReport` is an observation surface. It must not be used as a rollout
source of truth, and the reconciler writes only its own status. Rollout state
remains owned by `PromotionRun`, `Target`, and `Cluster` controllers.

## Metrics

When the controller reconciles a report, it records bounded Prometheus gauges:

| Metric | Labels | Meaning |
|---|---|---|
| `kapro_fleetdriftreport_targets` | `report`, `state` | Target counts for `total`, `current`, `drifted`, `pending`, `failed`, and `unknown`. |
| `kapro_fleetdriftreport_backend_objects` | `report`, `state` | Backend object counts for `total` and `drifted`. |
| `kapro_fleetdriftreport_phase` | `report`, `phase` | One-hot current phase over `Pending`, `Current`, `Drifted`, `Unknown`, and `Failed`. |

Use these for drift-duration alerts and dashboards. The only unbounded label is
the report name, so prefer one report per fleet, environment, or promotion
slice rather than one report per application instance.

## `max-drift` Gate

The built-in `max-drift` gate reads an existing `FleetDriftReport` and blocks a
promotion when drift exceeds the configured budget. It does not observe
clusters directly. Drift, missing reports, and stale observations return
`Inconclusive`, so the default template policy keeps retrying instead of
halting the stage.

```yaml
gate:
  mode: auto
  gate:
    templates:
      - name: prod-drift-budget
        type: max-drift
        args:
          - name: reportRef
            value: checkout-prod
          - name: maxDriftedTargets
            value: "0"
          - name: maxDriftedBackendObjects
            value: "0"
          - name: maxPendingTargets
            value: "0"
          - name: maxFailedTargets
            value: "0"
          - name: maxUnknownTargets
            value: "0"
          - name: maxAge
            value: 10m
```

Parameters:

- `reportRef` or `report` is required.
- `maxDriftedTargets`, `maxDriftedBackendObjects`, `maxPendingTargets`,
  `maxFailedTargets`, and `maxUnknownTargets` default to `0`.
- `maxAge` blocks and retries when `status.observedAt` is missing or stale.
- `allowMissing=true` and `allowStale=true` are explicit escape hatches for
  bootstrap and migration windows.
