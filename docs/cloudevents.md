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
| `KAPRO_EVENTS_SINK_TIMEOUT` | no | `10s` | Per-delivery context deadline |
| `KAPRO_EVENTS_SINK_MAX_RETRIES` | no | `3` | Linear-backoff retries on transient failures |
| `KAPRO_LIFECYCLE_INSECURE_WEBHOOKS` | no | unset | Set to `1` to allow http:// URLs (for in-cluster sinks) |

Example Deployment fragment:

```yaml
env:
  - name: KAPRO_EVENTS_SINK_URL
    value: https://argo-events-eventbus.argo-events.svc/kapro
  - name: KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE
    valueFrom:
      secretKeyRef:
        name: kapro-events-auth
        key: token
```

The sink is the **canonical** subscription point: one URL receives the
entire fleet feed, fans out to whatever notification/automation system
you already operate.

### 2. Per-Promotion handlers (ergonomic shortcut)

`Promotion.spec.lifecycle.handlers[]` lets a single Promotion declare
inline webhook or Kubernetes-Event handlers. Use this for one-off
integrations or when the team-level subscriber differs from the
fleet-wide one. See [generic-webhook.md](integrations/generic-webhook.md).

## CloudEvents v1.0 envelope

```jsonc
{
  "specversion": "1.0",
  "id": "f9c4d39c5a4d4eba9a6b8ee2c3d4f5a6",
  "type": "kapro.io/promotion.succeeded",
  "source": "/apis/kapro.io/v1alpha1/promotions/checkout",
  "subject": "checkout",
  "time": "2026-05-19T14:23:11.123456Z",
  "datacontenttype": "application/json",
  "data": {
    "promotion": "checkout",
    "promotionUID": "uid-abc",
    "kaproRef": "checkout-fleet",
    "phase": "Succeeded",
    "previousPhase": "Progressing",
    "version": "v1.2.3",
    "attemptName": "checkout-att-1",
    "reason": "Promotion phase: Progressing -> Succeeded",
    "message": ""
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
| `kapro.io/promotion.resumed` | Suspend cleared; non-Paused phase entered. | (custom) |
| `kapro.io/promotion.restarting` | A new attempt is stamped after a prior terminal attempt. | `restarting` |
| `kapro.io/promotion.succeeded` | Latest attempt converged. | `exited 0` |
| `kapro.io/promotion.failed` | Latest attempt failed terminally. | `exited >0` |
| `kapro.io/promotion.rollingBack` | (reserved) `spec.rollbackTo` triggered a rollback attempt. Reachable once that field ships. | — |
| `kapro.io/promotion.terminating` | `deletionTimestamp` set; GC draining child PromotionRuns. | `removing` |

### Per-attempt lifecycle

| Type | When |
|---|---|
| `kapro.io/promotion.attempt.stamped` | Controller created a new `PromotionRun` (first attempt or spec/template-hash change). |
| `kapro.io/promotion.attempt.superseded` | A previously non-terminal `PromotionRun` was marked `Superseded` because a newer attempt was stamped. |

### Reserved namespaces

These prefixes are reserved for future events. Subscribers should treat
unknown types under these prefixes as Kapro-shaped and ignore them
unless documented:

- `kapro.io/promotion.wave.*` — wave-level events
- `kapro.io/promotion.stage.*` — stage-level events (gate waiting/passed/failed)
- `kapro.io/promotion.target.*` — per-cluster events (applying, converged, failed)

## `data` field schema

| Field | Type | Stable | Meaning |
|---|---|---|---|
| `promotion` | string | yes | `Promotion.metadata.name` |
| `promotionUID` | string | yes | `Promotion.metadata.uid` for forensics across renames |
| `kaproRef` | string | yes | Parent `Kapro` fleet name (`Promotion.spec.kaproRef`) |
| `phase` | string | yes | `Promotion.status.phase` at emit |
| `previousPhase` | string | yes | Prior `status.phase`, empty for the initial transition |
| `version` | string | yes | `Promotion.spec.version` |
| `attemptName` | string | yes | The active `PromotionRun` name, when one exists |
| `reason` | string | yes | Short machine-readable cause |
| `message` | string | yes | One-line human summary |

## Delivery semantics

- **At-least-once.** Re-delivery may occur on controller restart. Subscribers must be idempotent. The CloudEvents `id` and the `(promotion, phase, attemptName)` tuple in `data` are the idempotency keys.
- **Order is not guaranteed.** Goroutines deliver concurrently. Use the `time` field plus `(previousPhase, phase)` to reconstruct causality.
- **Per-handler retry policy.** Linear backoff on transient failures (5xx, network errors). Permanent failures (4xx except 408/425/429, TLS/x509, malformed URLs) short-circuit.
- **SSRF guard.** Outbound URLs reject loopback/private/link-local/metadata addresses unless `KAPRO_LIFECYCLE_INSECURE_WEBHOOKS=1` is set.

## Go SDK

```go
import "kapro.io/kapro/pkg/events"

_ = events.EventPromotionSucceeded // == "kapro.io/promotion.succeeded"

body, env, err := events.Render(events.Event{
    Type:          events.EventPromotionSucceeded,
    PromotionName: "checkout",
    Phase:         "Succeeded",
    PreviousPhase: "Progressing",
    Version:       "v1.2.3",
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
