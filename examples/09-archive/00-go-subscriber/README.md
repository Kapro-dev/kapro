# Archive Kapro CloudEvents with a Go subscriber

This recipe uses the public `kapro.io/kapro/pkg/kapro` subscriber helper for a
custom audit store.

Run locally:

```sh
go run ./examples/09-archive/00-go-subscriber
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/09-archive/00-go-subscriber/sample-event.json
```

Point the operator at the subscriber service:

```sh
KAPRO_EVENTS_SINK_URL=http://kapro-archive.kapro-events.svc:8080/
```

Replace the stdout handler in `main.go` with writes to your database, queue, or
retention-controlled object store.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/09-archive/00-go-subscriber/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/09-archive/00-go-subscriber/run.sh test
examples/09-archive/00-go-subscriber/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

```bash
kubectl delete -f examples/09-archive/00-go-subscriber --ignore-not-found
```
