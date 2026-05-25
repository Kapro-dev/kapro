# Archive Kapro CloudEvents with Argo Events or Knative Eventing

Use this recipe when your cluster already has event-routing infrastructure.
Kapro publishes CloudEvents over HTTP; the eventing layer receives them and
routes them to your archive backend.

For Knative Eventing, create a Broker and point Kapro at the broker ingress:

```sh
kubectl apply -n kapro-events -f examples/09-archive/01-eventing/knative-broker.yaml
KAPRO_EVENTS_SINK_URL=http://broker-ingress.knative-eventing.svc.cluster.local/kapro-events/kapro-archive
```

For Argo Events, use the webhook EventSource and Sensor:

```sh
kubectl apply -n kapro-events -f examples/09-archive/01-eventing/argo-events-webhook.yaml
KAPRO_EVENTS_SINK_URL=http://kapro-archive-eventsource-svc.kapro-events.svc:12000/kapro
```

The sample Sensor logs the event. Replace that trigger with your destination:
Kafka, S3 writer, workflow, or another internal archive service.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/09-archive/01-eventing/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/09-archive/01-eventing/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/09-archive/01-eventing --ignore-not-found
```
