# Kapro Go SDK Examples

These examples show the public `kapro.io/kapro/pkg/kapro` SDK surface.

## Promote with builder

`promote-with-builder` creates a `Promotion` with the fluent builder and writes
it to a controller-runtime fake client. It is safe to run without a Kubernetes
cluster.

```bash
go run ./examples/sdk-go/promote-with-builder
```

## CloudEvents subscriber

`cloudevents-subscriber` starts an HTTP sink for Kapro lifecycle CloudEvents and
registers a handler for successful promotions.

```bash
go run ./examples/sdk-go/cloudevents-subscriber
```
