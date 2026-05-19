# Kapro CloudEvents Vocabulary

Kapro publishes its fleet-promotion lifecycle as **CloudEvents v1.0**.
Any CloudEvents-aware system can subscribe — Argo Events, Flux
Notification Controller, kube-event-exporter, Knative Eventing,
Apache Camel K, AWS EventBridge, Google Eventarc, Azure Event Grid,
or a plain HTTP webhook.

This document is the **stable contract**. Constants are versioned with
Kapro's `v1alpha1` API: once a CloudEvents `type` is published it will
not be renamed or removed without a major API version bump.

## Subscribe in one of two ways

### 1. Operator-level sink (canonical)

Set environment variables on the `kapro-operator` Deployment:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `KAPRO_EVENTS_SINK_URL` | yes (to enable) | unset | HTTPS endpoint that receives every event |
| `KAPRO_EVENTS_SINK_AUTH_HEADER_NAME` | no | `Authorization` | Header name for the auth value |
| `KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE` | no | unset | Header value (e.g. `Bearer xoxb-…`). Source from a Secret via `valueFrom.secretKeyRef`. |
| `KAPRO_EVENTS_SINK_TIMEOUT` | no | `10s` | **Total** per-event budget — initial attempt + all retries + backoff sleeps must complete within this window. |
| `KAPRO_EVENTS_SINK_MAX_RETRIES` | no | `3` | Linear-backoff retries on transient failures |
| `KAPRO_LIFECYCLE_INSECURE_WEBHOOKS` | no | unset | Set to `1` to allow http:// URLs (for in-cluster sinks) |

The sink is the **canonical** subscription point: one URL receives the
entire fleet feed, including the fleet-narrative events
(`kapro.io/promotion.wave.*`, `.stage.*`, `.stage.gate.*`) that the
per-Promotion handler path does not deliver.

### 2. Per-Promotion handlers (ergonomic shortcut)

`Promotion.spec.lifecycle.handlers[]` lets a single Promotion declare
inline webhook or Kubernetes-Event handlers. Handlers fire on coarse
phase transitions (`Pending`, `Progressing`, ..., `Terminating`). They
do NOT fire on the finer-grained `wave.*` / `stage.*` / `stage.gate.*`
events — for those, use the operator-level sink and route downstream
via Argo Events / Flux Notification Controller / Knative.

The per-Promotion webhook payload is **identical** to the operator-level
sink envelope — both built via `pkg/events.Render`.

## CloudEvents v1.0 envelope

```jsonc
{
  "specversion": "1.0",
  "id": "f9c4d39c5a4d4eba9a6b8ee2c3d4f5a6",
  "type": "kapro.io/promotion.stage.gate.passed",
  "source": "/apis/kapro.io/v1alpha1/promotions/checkout",
  "subject": "checkout",
  "time": "2026-05-19T14:23:11.123456789Z",
  "datacontenttype": "application/json",
  "data": {
    "promotion": "checkout",
    "kaproRef": "checkout-fleet",
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

All fields are stable. New fields may be added; existing fields will
not change shape within `v1alpha1`.

## Event types (vocabulary)

Each `type` follows reverse-DNS naming under `kapro.io/`.

### Whole-Promotion lifecycle

| Type | When | Docker analogue |
|---|---|---|
| `kapro.io/promotion.created` | Controller first observes the Promotion (transition into `Pending`). | `created` |
| `kapro.io/promotion.progressing` | An attempt is rolling out. | `running` |
| `kapro.io/promotion.paused` | `spec.suspended=true` observed. | `paused` |
| `kapro.io/promotion.resumed` | Synthetic; fires on every transition out of `Paused`. | (custom) |
| `kapro.io/promotion.restarting` | A new attempt is stamped after a prior terminal attempt. | `restarting` |
| `kapro.io/promotion.succeeded` | Latest attempt converged. | `exited 0` |
| `kapro.io/promotion.failed` | Latest attempt failed terminally. | `exited >0` |
| `kapro.io/promotion.rollingBack` | (reserved) `spec.rollbackTo` triggered a rollback attempt. | — |
| `kapro.io/promotion.terminating` | `deletionTimestamp` set; GC draining child PromotionRuns. | `removing` |

### Per-attempt lifecycle

| Type | When |
|---|---|
| `kapro.io/promotion.attempt.stamped` | Controller created a new `PromotionRun`. |
| `kapro.io/promotion.attempt.superseded` | A non-terminal `PromotionRun` was marked `Superseded` because a newer attempt was stamped. |

### Wave-level (PromotionPlan DAG node)

| Type | When | data fields |
|---|---|---|
| `kapro.io/promotion.wave.entered` | A PromotionPlan node transitions from Pending to Progressing. | wave |
| `kapro.io/promotion.wave.completed` | A PromotionPlan node reaches terminal phase. | wave, reason (canonical `complete` or `failed`), message (human sentence) |

### Stage-level (Stage inside a PromotionPlan)

| Type | When | data fields |
|---|---|---|
| `kapro.io/promotion.stage.entered` | A Stage transitions from Pending to Progressing (first target started). | wave, stage |
| `kapro.io/promotion.stage.completed` | Every target in a Stage reached Converged. | wave, stage |

### Stage gate events (per-target — gates evaluate per cluster)

| Type | When | data fields |
|---|---|---|
| `kapro.io/promotion.stage.gate.waiting` | A gate begins evaluation for a target. | wave, stage, gate, target |
| `kapro.io/promotion.stage.gate.passed` | Gate returns Passed for a target. | wave, stage, gate, target, message |
| `kapro.io/promotion.stage.gate.failed` | Gate returns Failed (terminal — retries exhausted or failurePolicy=halt). | wave, stage, gate, target, message |

### Reserved namespaces

These prefixes are reserved for future events. Subscribers should treat
unknown types under these prefixes as Kapro-shaped:

- `kapro.io/promotion.target.*` — per-cluster events (applying, converged, failed)

## `data` field schema

| Field | Stable | Meaning |
|---|---|---|
| `promotion` | yes | `Promotion.metadata.name` |
| `promotionUID` | yes | `Promotion.metadata.uid` |
| `kaproRef` | yes | Parent `Kapro` fleet name |
| `phase` | yes | `Promotion.status.phase` for whole-Promotion / attempt events; `PromotionRun.status.phase` for wave / stage / stage.gate / target events. The scoped phase (wave/stage/gate local state) is in the event type plus the `wave` / `stage` / `gate` fields — never overloaded into `phase`. |
| `previousPhase` | yes | Prior `status.phase`, empty for the initial transition and for run-scoped events |
| `version` | yes | `Promotion.spec.version` |
| `attemptName` | yes | Active or just-affected `PromotionRun` name |
| `wave` | yes | PromotionPlan DAG node name (set on wave/stage/gate events) |
| `stage` | yes | Stage name within the PromotionPlan (set on stage/gate events) |
| `gate` | yes | Gate name (set on `stage.gate.*` events) |
| `target` | yes | FleetCluster name (set on per-target events) |
| `reason` | yes | Short machine-readable cause |
| `message` | yes | One-line human summary |

## Delivery semantics

- **At-least-once.** Re-delivery may occur on controller restart. Subscribers must be idempotent. The CloudEvents `id` plus `(promotion, phase, attemptName, wave, stage, gate, target)` are the idempotency keys.
- **Order is not guaranteed.** Goroutines deliver concurrently. Use `time` plus `(wave, stage)` to reconstruct causality.
- **Per-event total timeout.** `KAPRO_EVENTS_SINK_TIMEOUT` bounds the entire dispatch.
- **Retry policy.** Linear backoff on transient failures. Permanent failures (4xx except 408/425/429, TLS/x509, malformed URLs) short-circuit.
- **SSRF guard.** Outbound URLs reject loopback/private/link-local/metadata addresses unless `KAPRO_LIFECYCLE_INSECURE_WEBHOOKS=1`.
- **Transition guards.** Wave and stage events fire exactly once per phase edge — controller code uses `previousPromotionPlanPhase` / `previousStagePhase` helpers to dedupe across reconciles.

## Go SDK

```go
import "kapro.io/kapro/pkg/events"

_ = events.EventPromotionStageGatePassed // == "kapro.io/promotion.stage.gate.passed"

body, env, err := events.Render(events.Event{
    Type:          events.EventPromotionStageEntered,
    PromotionName: "checkout",
    Phase:         "Progressing",
    Version:       "v1.2.3",
    Wave:          "default",
    Stage:         "canary",
})
```

`events.AllEventTypes()` returns the complete vocabulary in declaration order.

## Versioning policy

- `pkg/events.EventType` constants and `pkg/events.EventData` field names are part of the **public API**.
- They follow the same compatibility guarantees as `kapro.io/v1alpha1` CRDs: stable within `v1alpha1`, may be renamed only at a major version bump.
- New `EventType` constants may be added in minor releases. Subscribers should treat unknown types as Kapro-shaped and ignore them safely.

## See also

- [Argo Events integration](integrations/argo-events.md)
- [Flux Notification Controller integration](integrations/flux-notification-controller.md)
- [kube-event-exporter integration](integrations/kube-event-exporter.md)
- [Generic webhook integration](integrations/generic-webhook.md)
