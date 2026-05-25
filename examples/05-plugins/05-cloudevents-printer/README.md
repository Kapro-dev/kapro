# cloudevents-printer — reference subscriber for Kapro CloudEvents

`cloudevents-printer` is the smallest useful artefact you can run to
verify Kapro's CloudEvents v1.0 sink end-to-end. It is **not** a
notification router — for Slack/Teams/PagerDuty/email use Argo CD
Notifications or Flux Notification Controller and point them at Kapro
(see [ADR-0003](../../../docs/adr/0003-cloudevents-publisher-posture.md)).

## What it does

- Listens for HTTP POSTs from `kapro-operator`'s CloudEvents sink. (TLS is intentionally not handled in-binary — terminate at an Ingress or service-mesh sidecar in production.)
- Decodes each request body as a CloudEvents v1.0 structured-mode
  envelope using `kapro.io/kapro/pkg/events.Envelope`.
- Pretty-prints one line per event to `stdout` in a fleet-narrative
  shape — `kubectl logs deploy/cloudevents-printer` becomes a live
  feed of every promotion phase, wave entry, stage completion, and
  gate transition across the fleet.
- Responds `204 No Content` on success, `400` on malformed input,
  `401` when `KAPRO_PRINTER_AUTH_HEADER` is set and the inbound
  `X-Kapro-Auth` header does not match.

## Why this exists

Three reasons:

1. **Validate the public contract from outside the operator.** Bugs
   in `pkg/events.Envelope` or in the sink path show up here
   immediately, before a paying user notices.
2. **Copy-paste starter.** "How do I consume Kapro CloudEvents in Go?"
   answer is `main.go` in this directory. Single dependency on
   `kapro.io/kapro/pkg/events`.
3. **Required artefact for CNCF Sandbox.** Demonstrates an external
   consumer of the public API.

## Quick start (local)

```sh
go run ./examples/05-plugins/05-cloudevents-printer
```

In another terminal, replay a sample event:

```sh
curl -sS -X POST -H 'Content-Type: application/cloudevents+json' \
  --data '{
    "specversion": "1.0",
    "id": "f9c4d39c5a4d4eba9a6b8ee2c3d4f5a6",
    "type": "kapro.io/promotion.stage.gate.passed",
    "source": "/apis/kapro.io/v1alpha1/promotions/checkout",
    "subject": "checkout",
    "time": "2026-05-19T22:00:00Z",
    "datacontenttype": "application/json",
    "data": {
      "promotion": "checkout",
      "fleetRef": "checkout-fleet",
      "phase": "Progressing",
      "version": "v1.2.3",
      "attemptName": "checkout-att-1",
      "wave": "default",
      "stage": "canary",
      "gate": "metrics",
      "target": "fi-prod",
      "reason": "gate passed"
    }
  }' \
  http://localhost:8080/
```

The printer prints something like:

```
2026-05-19T22:00:00Z stage.gate.passed          promo=checkout wave=default stage=canary gate=metrics target=fi-prod phase=Progressing version=v1.2.3 reason=gate passed
```

## In-cluster deployment

1. Build & push the image (or use the published `ghcr.io/kapro-dev/cloudevents-printer:latest`):

   ```sh
   docker build -f examples/05-plugins/05-cloudevents-printer/Dockerfile \
     -t ghcr.io/kapro-dev/cloudevents-printer:latest .
   docker push ghcr.io/kapro-dev/cloudevents-printer:latest
   ```

2. Apply the Deployment + Service into the namespace where you want
   the printer to live (the manifest does not pin a namespace; pick
   one with `kubectl apply -n <ns>`):

   ```sh
   kubectl apply -n kapro-events -f examples/05-plugins/05-cloudevents-printer/manifests/deployment.yaml
   ```

3. Point the Kapro operator at the printer's Service via the
   standard sink env vars on the `kapro-operator` Deployment:

   ```yaml
   env:
     - name: KAPRO_EVENTS_SINK_URL
       value: http://cloudevents-printer.kapro-events.svc:8080/
     - name: KAPRO_LIFECYCLE_INSECURE_WEBHOOKS
       value: "1"   # in-cluster service, no TLS
   ```

4. Watch the feed:

   ```sh
   kubectl logs -n kapro-events deploy/cloudevents-printer -f
   ```

## Configuration

| Variable / flag | Default | Purpose |
|---|---|---|
| `--listen` / `KAPRO_PRINTER_LISTEN_ADDR` | `:8080` | HTTP bind address |
| `KAPRO_PRINTER_AUTH_HEADER` | unset | When set, inbound requests must carry a matching `X-Kapro-Auth` header. Pair with `KAPRO_EVENTS_SINK_AUTH_HEADER_NAME=X-Kapro-Auth` + a Secret-backed `KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE` on the operator. |

## What this is NOT

- **Not a notification router.** It writes to `stdout`, full stop.
  Slack/Teams/PagerDuty/email are out of scope by design — that's
  what Argo CD Notifications and Flux Notification Controller exist
  for. See [ADR-0003](../../../docs/adr/0003-cloudevents-publisher-posture.md).
- **Not a persistent store.** Restart loses the buffer. Pipe `stdout`
  to a log aggregator if you want retention.
- **Not authenticated by default.** The shared-secret header is a
  smoke check, not a real auth layer. In production, front this with
  an Ingress that enforces mTLS or an OAuth proxy.

## How the contract is validated

`go test ./examples/05-plugins/05-cloudevents-printer/...` includes a sweep
test (`TestHandleAcceptsEveryEventType`) that walks every constant in
`events.AllEventTypes()`, renders it, and POSTs the body through the
real `server.handle` — same JSON parser, same `specversion` check,
same `204` response path that production traffic hits. New event
types in `pkg/events` are exercised automatically.

## See also

- [`docs/events.md`](../../../docs/concepts/events.md) — vocabulary spec
- [`docs/adr/0003-cloudevents-publisher-posture.md`](../../../docs/adr/0003-cloudevents-publisher-posture.md) — why Kapro doesn't ship Slack substrates

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/05-plugins/05-cloudevents-printer/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/05-plugins/05-cloudevents-printer/run.sh test
examples/05-plugins/05-cloudevents-printer/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
