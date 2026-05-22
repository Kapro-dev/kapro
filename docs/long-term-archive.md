# Long-term Promotion Archive

Kapro keeps recent PromotionRun state in Kubernetes. Long-lived audit history
belongs outside etcd: point the operator-level CloudEvents sink at an archive
receiver and let your existing observability or object-storage stack keep the
retention window you need.

The operator remains unchanged. Set `KAPRO_EVENTS_SINK_URL` on the operator to
an HTTP receiver that accepts CloudEvents v1.0 structured JSON:

```sh
KAPRO_EVENTS_SINK_URL=http://kapro-archive.kapro-events.svc:8080/
```

## Archive contract

Kapro's CloudEvents stream is the export surface for promotion history. Archive
receivers should store the original request body and index CloudEvents metadata:

- `id`, `source`, `type`, `subject`, `time`
- `datacontenttype`
- `data.promotion`, `data.phase`, `data.version`, `data.wave`, `data.stage`,
  `data.gate`, and `data.target` when present

Use the CloudEvents `id` plus `source` and `type` as the dedupe key. Kapro may
retry sink delivery after a non-2xx response.

## Cookbook recipes

### Vector to Elasticsearch, Loki, or Splunk

Use `examples/archive-vector/` when Vector already fronts your log archive:

```sh
vector --config examples/archive-vector/vector.yaml
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/archive-vector/sample-event.json
```

In cluster, expose Vector as a Service and set:

```sh
KAPRO_EVENTS_SINK_URL=http://vector.kapro-events.svc:8080/
```

### Fluent Bit to CloudWatch or Google Cloud Logging

Use `examples/archive-fluentbit/` when Fluent Bit is your standard log router:

```sh
fluent-bit -c examples/archive-fluentbit/fluent-bit.conf
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/archive-fluentbit/sample-event.json
```

Enable the CloudWatch or Stackdriver output block in the example for production.

### Argo Events or Knative Eventing

Use `examples/archive-eventing/` when the cluster already has event-router
infrastructure:

```sh
kubectl apply -n kapro-events -f examples/archive-eventing/knative-broker.yaml
kubectl apply -n kapro-events -f examples/archive-eventing/argo-events-webhook.yaml
```

Route the trigger to your archive writer, queue, workflow, or data platform.

### Custom Go subscriber

Use `examples/archive-go-subscriber/` as the smallest bespoke archive receiver:

```sh
go run ./examples/archive-go-subscriber
curl -sS -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/cloudevents+json' \
  --data @examples/archive-go-subscriber/sample-event.json
```

The example uses `kapro.io/kapro/pkg/kapro.Subscriber`; replace the stdout
handler with writes to your audit store.

## Optional `kapro-archiver`

Kapro also ships an opt-in reference subscriber at `cmd/kapro-archiver` and a
separate Helm chart at `charts/kapro-archiver`. It is not part of the operator
binary and is not installed by default.

File sink:

```sh
helm install kapro-archiver ./charts/kapro-archiver \
  --namespace kapro-system \
  --set persistence.enabled=true
```

S3 sink:

```sh
helm install kapro-archiver ./charts/kapro-archiver \
  --namespace kapro-system \
  --set archiver.sink=s3 \
  --set s3.bucket=my-kapro-archive \
  --set s3.region=us-east-1
```

The archiver stores:

- `event.json`: the original CloudEvents request body
- `metadata.json`: CloudEvents metadata, request metadata, body SHA-256, and
  dedupe key

Objects are keyed by event type, event date, source hash, and CloudEvents ID.
Duplicate deliveries with the same key are treated as already archived.

## Retention and deletion

Archive retention is controlled by the destination system, not by Kapro. If a
Promotion is deleted from Kubernetes, archived CloudEvents may still exist in
Elasticsearch, Loki, Splunk, CloudWatch, S3, or another backend. Set tenant and
compliance retention policies on the archive side, including explicit deletion
workflows when required.
