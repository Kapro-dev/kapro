# CloudEvents Printer Manifests

Deployment and Service for the CloudEvents printer example.

```text
Kapro lifecycle events -> Service -> cloudevents-printer logs
```

Apply into the namespace that should receive events:

```bash
kubectl apply -n kapro-events -f examples/05-plugins/05-cloudevents-printer/manifests/deployment.yaml
```
