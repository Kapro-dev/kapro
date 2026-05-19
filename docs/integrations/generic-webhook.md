# Generic Webhook ← Kapro

Two webhook delivery paths exist; pick based on scope.

## A. Per-Promotion handler (in-CRD)

For one-off integrations declared inline on a single Promotion:

```yaml
apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: checkout
spec:
  kaproRef: checkout
  version: v1.2.3
  lifecycle:
    handlers:
      - name: pager-on-failure
        on: [Failed]
        webhook:
          url: https://events.pagerduty.com/v2/enqueue
          authHeader:
            name: Authorization
            secretName: pagerduty
            secretKey: routing-key
        timeout: 30s
        maxRetries: 5
```

- Fires only for the named Promotion.
- Uses the **same CloudEvents v1.0 envelope** as the operator-level sink — both built via `pkg/events.Render`. Subscribers can swap which path delivered a given event without changing how they parse it.
- Outcome recorded in `Promotion.status.lifecycleHandlerResults[]`.

## B. Operator-level sink (cluster-wide)

For a single subscriber that should receive **every** Promotion event
across the fleet:

```yaml
# kapro-operator Deployment env
- name: KAPRO_EVENTS_SINK_URL
  value: https://events.example.com/kapro
- name: KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE
  valueFrom:
    secretKeyRef:
      name: kapro-events
      key: token
- name: KAPRO_EVENTS_SINK_TIMEOUT
  value: 10s
- name: KAPRO_EVENTS_SINK_MAX_RETRIES
  value: "3"
```

- Fires for **every** Promotion in the cluster.
- One request per transition.
- Same CloudEvents v1.0 envelope as path A.
- Outcome surfaced via Kubernetes Events (`EventSinkDelivered` /
  `EventSinkFailed`) and Prometheus metrics
  (`kapro_lifecycle_hook_invocations_total{kind="Sink",...}`).

## Receiver-side expectations

- Treat the body as `application/cloudevents+json` (CloudEvents v1.0
  structured mode).
- Respond `2xx` for success.
- Respond `5xx` / connection-reset for transient failures (Kapro
  retries with linear backoff).
- Respond `4xx` (except 408/425/429) for permanent rejections
  (Kapro stops retrying).
- Be **idempotent**. Kapro guarantees at-least-once delivery; dedupe on
  the CloudEvents `id` OR on `(promotion, phase, attemptName)` from
  `data`.

## Choosing between A and B

| | A (per-Promotion) | B (operator sink) |
|---|---|---|
| Scope | Single Promotion | Fleet-wide |
| Configured by | App owner (in their Promotion manifest) | Platform team (in operator Deployment env) |
| Multiple subscribers | Many handlers per Promotion | One sink — fan out downstream (Argo Events, Flux NC, …) |
| Idempotency state | `status.lifecycleHandlerResults[]` | None on Kapro side; subscriber must dedupe |
| Best for | Team-specific Slack channel, dev-only alerts | Bus into Argo Events / Flux Notification Controller / kube-event-exporter / Knative / cloud event bus |

For multi-backend production deployments, pick B and route via Argo
Events or Flux Notification Controller — see the dedicated docs.

The vocabulary is documented in [../cloudevents.md](../cloudevents.md).
