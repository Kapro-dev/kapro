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
go run ./examples/05-plugins/04-capacity-planner --listen :9090
```

## Registration

The standalone manifest is
`examples/05-plugins/04-capacity-planner/registration.yaml`.

```yaml
apiVersion: kapro.io/v1alpha1
kind: Plugin
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
go test ./examples/05-plugins/04-capacity-planner
```

The test suite runs the shared KPI conformance harness and planner-specific
tests.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/05-plugins/04-capacity-planner/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/05-plugins/04-capacity-planner/run.sh test
examples/05-plugins/04-capacity-planner/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

```bash
kubectl delete -f examples/05-plugins/04-capacity-planner --ignore-not-found
```
