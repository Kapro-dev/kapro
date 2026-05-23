# Decision Traces

`DecisionTrace` is Kapro's durable controller decision audit stream. It records
why the operator advanced, deferred, failed, rolled back, or skipped part of a
promotion without requiring access to controller logs.

`DecisionTrace` is cluster-scoped:

```yaml
apiVersion: kapro.io/v1alpha2
kind: DecisionTrace
metadata:
  name: dtrace-...
spec:
  promotionRun: checkout-7f4c9
  plan: canary
  stage: prod
  target: de-prod-01
  eventType: GateEvaluate
  source: slo
  phase: Failed
  reason: GateEvaluated
  message: error budget exhausted
  time: "2026-05-23T15:20:00Z"
```

## Event Types

| Event type | Meaning |
| --- | --- |
| `GateEvaluate` | Gate, metrics, soak, or approval evaluation decision. |
| `BatchProgress` | Planner or stage-strategy decision, including deferrals. |
| `Rollback` | Automatic rollback target creation or rollback action. |
| `Suspend` | PromotionRun, target, or cluster suspend prevented progress. |
| `Stage` | Stage entry, completion, failure policy, skip, or halt decision. |

Each trace includes `promotionRun`, `source`, `eventType`, `phase`, `reason`,
`message`, and optional `plan`, `stage`, `target`, and bounded `evidence`.
Evidence is intentionally small and non-secret. Long-term archive integrations
store full CloudEvents envelopes separately.

## Decision API Compatibility

`Target.status.decisionTrace` still stores the Decision API approval trace for a
single target. The `DecisionTrace` CRD is the durable cluster-wide controller
stream. Both surfaces can exist for the same promotion: one answers "what did an
agent or human decide for this approval gate?", and the other answers "what did
the operator decide across gates, stages, batches, rollback, and suspend paths?"

## Signing

`DecisionTrace.status.signed` and `status.signatureRef` are reserved for the
v0.3.x signing increment. v0.3.2 writes unsigned traces only.
