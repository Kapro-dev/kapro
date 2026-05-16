# Capacity Planner Plugin

This example implements the Kapro Planner Interface for capacity-aware target
selection. It runs as a gRPC server and returns one planning decision for each
candidate target.

Planner plugin runtime dispatch is not wired yet. Kapro probes planner
registrations and records readiness; built-in planning remains the runtime path
until external planner dispatch is implemented.

## Behavior

The planner evaluates targets in this order:

1. `ready=false` targets are skipped.
2. Targets with `active_promotionrun` are deferred.
3. Targets missing required labels are skipped.
4. Targets below `minAvailableCapacityPercent` are deferred.
5. Remaining targets are included, ordered by capacity descending, then region,
   then name.
6. `strategy.max_parallel` limits the number of included targets; eligible
   overflow targets are deferred.

Capacity is read from the first available label:

```text
kapro.io/available-capacity-percent
availableCapacityPercent
capacity
```

Missing capacity defaults to `100`.

## Parameters

| Name | Purpose |
|---|---|
| `minAvailableCapacityPercent` | Minimum capacity required to include a target. Defaults to `0`. |
| `requiredLabel.<key>` | Required target label value. Example: `requiredLabel.region: eu`. |

## Run

```bash
go run ./examples/plugins/capacity-planner --listen :9090
```

## Registration

The standalone manifest is
`examples/plugins/capacity-planner-registration.yaml`.

```yaml
apiVersion: kapro.io/v1alpha1
kind: PluginRegistration
metadata:
  name: capacity-planner
spec:
  type: planner
  name: capacity
  protocol: grpc
  endpoint: dns:///capacity-planner.kapro-system.svc:9090
  timeout: 10s
  parameters:
    minAvailableCapacityPercent: "20"
    requiredLabel.region: eu
```

## Verify

```bash
go test ./examples/plugins/capacity-planner
```

The test suite runs the shared KPI conformance harness and planner-specific
tests.
