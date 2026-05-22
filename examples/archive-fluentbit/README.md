# Archive Kapro CloudEvents with Fluent Bit

This recipe receives Kapro CloudEvents with Fluent Bit's HTTP input and forwards
them to a configured log archive such as CloudWatch Logs or Google Cloud
Logging.

Run a local smoke test:

```sh
fluent-bit -c examples/archive-fluentbit/fluent-bit.conf
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/archive-fluentbit/sample-event.json
```

In Kubernetes, point the operator at the Fluent Bit service:

```sh
KAPRO_EVENTS_SINK_URL=http://fluent-bit.kapro-events.svc:8080/
```

Keep the stdout output for smoke tests, then enable the CloudWatch or
Stackdriver output block for production.
