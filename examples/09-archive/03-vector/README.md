# Archive Kapro CloudEvents with Vector

This recipe receives Kapro CloudEvents over HTTP and lets Vector route them to
Elasticsearch, Loki, Splunk, or another configured sink.

Run a local smoke test:

```sh
vector --config examples/09-archive/03-vector/vector.yaml
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/09-archive/03-vector/sample-event.json
```

In Kubernetes, point the operator at the Vector service:

```sh
KAPRO_EVENTS_SINK_URL=http://vector.kapro-events.svc:8080/
```

Replace the placeholder sink in `vector.yaml` with your production backend.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/09-archive/03-vector/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/09-archive/03-vector/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/09-archive/03-vector --ignore-not-found
```
