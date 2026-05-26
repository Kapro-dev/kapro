# Events

Kapro publishes fleet-promotion lifecycle events as CloudEvents v1.0 envelopes.
The operator can send events to one fleet-wide sink, and individual Promotions
can also declare lightweight lifecycle handlers.

`pkg/events.EventType` constants are public integration contract. New event
types may be added, but existing `kapro.io/...` strings must not be renamed
within the `v1alpha1` API line.

## Subscribe

Operator-level sink:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `KAPRO_EVENTS_SINK_URL` | yes, to enable | unset | HTTPS endpoint that receives every event. |
| `KAPRO_EVENTS_SINK_AUTH_HEADER_NAME` | no | `Authorization` | Header name for auth. |
| `KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE` | no | unset | Header value, usually sourced from a Secret. |
| `KAPRO_EVENTS_SINK_TIMEOUT` | no | `10s` | Total per-event timeout across retries. |
| `KAPRO_EVENTS_SINK_MAX_RETRIES` | no | `3` | Linear-backoff retries for transient failures. |
| `KAPRO_LIFECYCLE_INSECURE_WEBHOOKS` | no | unset | Set to `1` only for local HTTP sinks. |

Per-Promotion handlers live under `Promotion.spec.lifecycle.handlers[]` and use
the same CloudEvents envelope, but they fire only for coarse Promotion phase
transitions.

## Envelope

```json
{
  "specversion": "1.0",
  "id": "f9c4d39c5a4d4eba9a6b8ee2c3d4f5a6",
  "type": "kapro.io/promotion.stage.gate.passed",
  "source": "/apis/kapro.io/v1alpha1/promotions/checkout",
  "subject": "checkout",
  "time": "2026-05-19T14:23:11Z",
  "datacontenttype": "application/json",
  "data": {
    "promotion": "checkout",
    "fleet": "checkout-fleet",
    "phase": "Progressing",
    "version": "v1.2.3",
    "attemptName": "checkout-att-1",
    "wave": "default",
    "stage": "canary",
    "gate": "metrics",
    "target": "fi-prod",
    "reason": "gate passed",
    "message": "Datadog SLO ok"
  }
}
```

## Event Types

| Type | When |
|---|---|
| `kapro.io/promotion.created` | Controller first observes the Promotion. |
| `kapro.io/promotion.progressing` | An attempt is rolling out. |
| `kapro.io/promotion.paused` | `spec.suspended=true` is observed. |
| `kapro.io/promotion.resumed` | A Promotion transitions out of paused state. |
| `kapro.io/promotion.restarting` | A new attempt is stamped after a terminal attempt. |
| `kapro.io/promotion.succeeded` | Latest attempt converged. |
| `kapro.io/promotion.failed` | Latest attempt failed terminally. |
| `kapro.io/promotion.rollingBack` | Reserved for rollback attempts. |
| `kapro.io/promotion.terminating` | Promotion deletion starts. |
| `kapro.io/promotion.attempt.stamped` | Controller created a new PromotionRun. |
| `kapro.io/promotion.attempt.superseded` | An older non-terminal PromotionRun was superseded. |
| `kapro.io/promotion.wave.entered` | A Plan DAG node starts. |
| `kapro.io/promotion.wave.completed` | A Plan DAG node reaches terminal phase. |
| `kapro.io/promotion.stage.entered` | A stage starts. |
| `kapro.io/promotion.stage.completed` | Every target in a stage converged. |
| `kapro.io/promotion.stage.gate.waiting` | A gate begins evaluation for a target. |
| `kapro.io/promotion.stage.gate.passed` | A gate passes for a target. |
| `kapro.io/promotion.stage.gate.failed` | A gate fails for a target. |

## Data Fields

| Field | Meaning |
|---|---|
| `promotion` | `Promotion.metadata.name`. |
| `promotionUID` | Kubernetes UID for traceability. |
| `fleet` | Parent `Fleet` name. |
| `phase` | Promotion phase for whole-Promotion and attempt events; PromotionRun phase for wave, stage, gate, and target events. |
| `previousPhase` | Prior phase for transition events. |
| `version` | Requested artifact version. |
| `attemptName` | Active or affected PromotionRun name. |
| `wave` | Plan DAG node name. |
| `stage` | Stage name inside a Plan. |
| `gate` | Gate name. |
| `target` | Cluster name. |
| `reason` | Short machine-readable cause. |
| `message` | One-line human summary. |

## Delivery Semantics

- Events are delivered at least once. Subscribers must be idempotent.
- Ordering is not guaranteed across concurrent dispatches.
- `KAPRO_EVENTS_SINK_TIMEOUT` bounds the whole dispatch attempt, including
  retries and backoff.
- 4xx responses other than retryable throttling/timeout statuses are treated as
  permanent failures.
- Outbound URLs reject loopback, private, link-local, and metadata addresses
  unless insecure local webhooks are explicitly enabled.

## Integration Patterns

| Integration | Pattern |
|---|---|
| Generic webhook | Point `KAPRO_EVENTS_SINK_URL` at an HTTPS receiver that accepts structured CloudEvents JSON. |
| Argo Events | Use a webhook EventSource and Sensor to route selected event types. |
| Flux Notification Controller | Point a Flux `Receiver` at Kapro's sink URL, then route with `Provider` and `Alert`. |
| kube-event-exporter | Route Kubernetes Events emitted by Kapro when HTTP sinks are not desired. |
| SIEM or audit store | Ingest all CloudEvents and index by `source`, `subject`, `type`, and `data.promotion`. |

## Per-cluster events: scope decision

Kapro does **not** emit per-cluster reconcile events
(`kapro.io/promotion.target.*` is intentionally not part of the
vocabulary). Per-cluster state belongs to the delivery tool that owns
the actual reconcile loop:

| Need | Where to subscribe |
|---|---|
| "Did cluster X converge on version Y?" | Flux Notification Controller `Alert` on `Kustomization`, or Argo CD Notifications on `Application` |
| "Which targets are still pending?" | `Target.status.phase` via the Kubernetes API |
| "Did gate Z pass for cluster X?" | `kapro.io/promotion.stage.gate.passed` — `data.target` carries the cluster |

Rationale is captured in
[ADR-0005](../adr/0005-withdraw-target-namespace.md).
