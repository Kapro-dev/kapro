# Archive Kapro CloudEvents with Fluent Bit

This recipe receives Kapro CloudEvents with Fluent Bit's HTTP input and forwards
them to a configured log archive such as CloudWatch Logs or Google Cloud
Logging.

Run a local smoke test:

```sh
fluent-bit -c examples/09-archive/02-fluentbit/fluent-bit.conf
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/09-archive/02-fluentbit/sample-event.json
```

In Kubernetes, point the operator at the Fluent Bit service:

```sh
KAPRO_EVENTS_SINK_URL=http://fluent-bit.kapro-events.svc:8080/
```

Keep the stdout output for smoke tests, then enable the CloudWatch or
Stackdriver output block for production.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/09-archive/02-fluentbit/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/09-archive/02-fluentbit/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/09-archive/02-fluentbit --ignore-not-found
```
