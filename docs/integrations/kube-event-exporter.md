# kube-event-exporter ← Kapro

[kube-event-exporter](https://github.com/resmoio/kubernetes-event-exporter)
routes Kubernetes `Event` objects to backends (Slack, Teams, ES,
Loki, Kafka, S3, etc.). Kapro emits Kubernetes Events on every
phase transition in addition to the CloudEvents sink, so this path
needs zero Kapro configuration beyond having the controller running.

## Filter on Kapro-specific Events

Kapro reasons that downstream subscribers can match on:

| Reason | When | Severity |
|---|---|---|
| `Pending` / `Progressing` / `Paused` / ... | Phase transition | Normal |
| `Failed` | Phase = Failed | Warning |
| `AttemptStamped` | New PromotionRun created under a Promotion | Normal |
| `LifecycleHookFired` | Per-Promotion handler fired | Normal |
| `LifecycleHookFailed` | Per-Promotion handler failed | Warning |
| `EventSinkDelivered` | Operator-level sink accepted an event | Normal |
| `EventSinkFailed` | Operator-level sink rejected an event | Warning |

## Example config: route Promotion failures to Slack

```yaml
# kube-event-exporter ConfigMap
route:
  routes:
    - match:
        - kind: "Promotion"
          apiGroup: "kapro.io"
          reason: "Failed"
      receiver: slack

receivers:
  - name: slack
    slack:
      channel: "#kapro-failures"
      token: "{{ .SLACK_TOKEN }}"
      message: |
        Promotion *{{ .InvolvedObject.Name }}* failed.
        {{ .Message }}
```

## When to use this vs the CloudEvents sink

| Use kube-event-exporter when | Use the CloudEvents sink when |
|---|---|
| You already operate it for cluster-wide event routing. | You want a typed, versioned vocabulary (CloudEvents). |
| Slack/Teams formatting is good enough. | You need to fan out to many subscribers with different filters. |
| You don't want to add KAPRO_EVENTS_SINK_URL. | You want the canonical fleet-promotion event feed. |

Both can run together: the sink delivers full CloudEvents to one place,
and kube-event-exporter routes legibly-formatted summaries to
chat backends.

See [../cloudevents.md](../cloudevents.md) for the typed event taxonomy.
