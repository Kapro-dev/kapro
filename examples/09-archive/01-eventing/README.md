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
