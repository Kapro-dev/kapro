# Argo Events ← Kapro

Subscribe to every Kapro fleet-promotion CloudEvent by pointing the
operator-level sink at an [Argo Events](https://argoproj.github.io/argo-events/)
[`Webhook` EventSource](https://argoproj.github.io/argo-events/eventsources/setup/webhook/).
The downstream `Sensor` can trigger anything Argo Events supports
(Argo Workflows, K8s objects, AWS Lambda, HTTP webhook, etc.).

## 1. EventSource

```yaml
apiVersion: argoproj.io/v1alpha1
kind: EventSource
metadata:
  name: kapro
  namespace: argo-events
spec:
  service:
    ports:
      - port: 12000
        targetPort: 12000
  webhook:
    kapro:
      port: "12000"
      endpoint: /kapro
      method: POST
```

## 2. Sensor — example: post Succeeded events to Slack

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Sensor
metadata:
  name: kapro-succeeded-to-slack
  namespace: argo-events
spec:
  template:
    serviceAccountName: argo-events-sa
  dependencies:
    - name: kapro-succeeded
      eventSourceName: kapro
      eventName: kapro
      filters:
        data:
          - path: type
            type: string
            value:
              - kapro.io/promotion.succeeded
  triggers:
    - template:
        name: notify-slack
        http:
          url: https://hooks.slack.com/services/T00/B00/XXX
          method: POST
          payload:
            - src:
                dependencyName: kapro-succeeded
                dataKey: data.promotion
              dest: text
```

## 3. Configure Kapro

```yaml
# kapro-operator Deployment env
- name: KAPRO_EVENTS_SINK_URL
  value: http://kapro-eventsource-svc.argo-events.svc:12000/kapro
- name: KAPRO_LIFECYCLE_INSECURE_WEBHOOKS
  value: "1"   # in-cluster service, no TLS required
```

## Useful filters

`spec.dependencies[].filters.data[].path` examples:

- `type` — match a specific event type
- `data.kaproRef` — match a specific Kapro fleet
- `data.phase` — match a phase (e.g. `Failed`)
- `data.version` — match a version pattern

See the full vocabulary in [../cloudevents.md](../cloudevents.md).
