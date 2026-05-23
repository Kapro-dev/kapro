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
| `Delivery` | Hub-observed spoke delivery status, including OCI staging and commit diagnostics. |

Each trace includes `promotionRun`, `source`, `eventType`, `phase`, `reason`,
`message`, and optional `plan`, `stage`, `target`, and bounded `evidence`.
Evidence is intentionally small and non-secret. Long-term archive integrations
store full CloudEvents envelopes separately.

Delivery traces are emitted by the hub operator from observed
`Cluster.status.delivery`, not directly by spokes. This keeps per-cluster RBAC
narrow and lets optional DecisionTrace signing remain centralized. For OCI
spoke delivery, evidence includes the app key, desired version, observed digest,
artifact format, applied object count, and staged-delivery counts such as
`stagedObjects`, `stagingFailedObjects`, `committedObjects`,
`commitFailedObjects`, and `stagingFailurePhase`.

## Explain a PromotionRun

Use `kapro why` to read the DecisionTrace stream for one PromotionRun:

```bash
kapro why checkout-7f4c9
kapro why checkout-7f4c9 -o json
```

The command lists records by the `kapro.io/promotionrun` label written by the
operator. The text view prints a chronological timeline with event type, phase,
reason, scope, source, signing state, and message. JSON output is intended for
tooling that wants to turn the same trace list into incident reports or
dashboards.

## Reconstruct At A Time

Use `kapro reconstruct` when you need the latest known controller decisions at a
specific point in time:

```bash
kapro reconstruct checkout-7f4c9 --at 2026-05-23T15:30:00Z
kapro reconstruct checkout-7f4c9 --at 2026-05-23T15:30:00Z -o json
```

The command replays `DecisionTrace` records for the PromotionRun up to `--at`
and summarizes the latest record per scope. The text output is optimized for
operators; JSON includes the filtered timeline for tools that need richer
reconstruction or incident-report rendering. This is trace reconstruction
groundwork, not full Kubernetes object replay from an external archive.

## Decision API Compatibility

`Target.status.decisionTrace` still stores the Decision API approval trace for a
single target. The `DecisionTrace` CRD is the durable cluster-wide controller
stream. Both surfaces can exist for the same promotion: one answers "what did an
agent or human decide for this approval gate?", and the other answers "what did
the operator decide across gates, stages, batches, rollback, and suspend paths?"

## Signing

DecisionTrace signing is optional. When `KAPRO_DECISIONTRACE_SIGNING_KEY_FILE`
points at a PEM-encoded PKCS#8 Ed25519 private key, the controller signs the
canonical `spec` payload and writes a detached signature to status:

```yaml
status:
  signed: true
  signatureAlgorithm: Ed25519
  signatureKeyID: prod-audit-key
  payloadDigest: sha256:...
  signature: ...
```

Set `KAPRO_DECISIONTRACE_SIGNING_KEY_ID` to the verifier-facing key identity.
If it is unset, Kapro uses the key file basename. The operator still runs and
writes unsigned traces when no signing key is configured.

The signed payload is domain-separated with Kapro's DecisionTrace message type
and intentionally excludes Kubernetes metadata and status. That makes
verification stable across label changes, resourceVersion updates, and future
status fields. Evidence should still be treated as bounded audit context, not
as a secret or full event archive.

Example key generation:

```bash
openssl genpkey -algorithm ED25519 -out decisiontrace-ed25519.pem
```

`status.signatureRef` remains reserved for an external transparency-log record,
such as Rekor, once the Sigstore backend lands.
