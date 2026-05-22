# Archive Kapro CloudEvents with a Go subscriber

This recipe uses the public `kapro.io/kapro/pkg/kapro` subscriber helper for a
custom audit store.

Run locally:

```sh
go run ./examples/archive-go-subscriber
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/archive-go-subscriber/sample-event.json
```

Point the operator at the subscriber service:

```sh
KAPRO_EVENTS_SINK_URL=http://kapro-archive.kapro-events.svc:8080/
```

Replace the stdout handler in `main.go` with writes to your database, queue, or
retention-controlled object store.
