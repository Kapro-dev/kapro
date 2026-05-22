# Archive Kapro CloudEvents with Vector

This recipe receives Kapro CloudEvents over HTTP and lets Vector route them to
Elasticsearch, Loki, Splunk, or another configured sink.

Run a local smoke test:

```sh
vector --config examples/archive-vector/vector.yaml
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/archive-vector/sample-event.json
```

In Kubernetes, point the operator at the Vector service:

```sh
KAPRO_EVENTS_SINK_URL=http://vector.kapro-events.svc:8080/
```

Replace the placeholder sink in `vector.yaml` with your production backend.
