# Flux Notification Controller ← Kapro

[Flux Notification Controller](https://fluxcd.io/flux/components/notification/)
routes events to Slack, Teams, PagerDuty, OpsGenie, MS Graph, and many other
backends. Subscribe Flux to Kapro by configuring the operator-level sink to
deliver to a Flux Receiver.

## 1. Flux Receiver

Receivers expose an HTTPS endpoint that triggers downstream Flux
notifications:

```yaml
apiVersion: notification.toolkit.fluxcd.io/v1
kind: Receiver
metadata:
  name: kapro-events
  namespace: flux-system
spec:
  type: generic
  secretRef:
    name: kapro-events-token
  resources:
    - apiVersion: kapro.io/v1alpha1
      kind: Promotion
---
apiVersion: v1
kind: Secret
metadata:
  name: kapro-events-token
  namespace: flux-system
stringData:
  token: <generated-token>
```

Note the receiver URL: `https://flux-webhook.example.com/hook/<receiver-id>`.

## 2. Provider + Alert

```yaml
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata:
  name: slack
  namespace: flux-system
spec:
  type: slack
  channel: deployments
  secretRef:
    name: slack-webhook
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata:
  name: kapro-failures
  namespace: flux-system
spec:
  providerRef:
    name: slack
  eventSeverity: error
  eventSources:
    - kind: Promotion
      apiVersion: kapro.io/v1alpha1
      name: '*'
  inclusionList:
    - kapro\.io/promotion\.failed
```

## 3. Configure Kapro

```yaml
# kapro-operator Deployment env
- name: KAPRO_EVENTS_SINK_URL
  value: https://flux-webhook.example.com/hook/<receiver-id>
- name: KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE
  valueFrom:
    secretKeyRef:
      name: kapro-flux-receiver-token
      key: token
- name: KAPRO_EVENTS_SINK_AUTH_HEADER_NAME
  value: X-Signature   # matches Flux Receiver `generic` type
```

## Mapping Kapro CloudEvents to Flux Alerts

Flux's `Alert.spec.inclusionList` accepts Go regexes on the `type` field
of the received CloudEvents. Compose targeted alerts:

| Goal | `inclusionList` regex |
|---|---|
| All failures, fleet-wide | `kapro\.io/promotion\.failed` |
| Successes for one fleet | (combine with `eventSources.name` filter on the Promotion name) |
| Every transition | `kapro\.io/promotion\.[a-z]+` |

The vocabulary lives in [../cloudevents.md](../cloudevents.md).
