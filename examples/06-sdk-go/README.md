# Kapro Go SDK Examples

These examples show the public `kapro.io/kapro/pkg/kapro` SDK surface.

## Promote with builder

`promote-with-builder` creates a `Promotion` with the fluent builder and writes
it to a controller-runtime fake client. It is safe to run without a Kubernetes
cluster.

```bash
go run ./examples/06-sdk-go/00-promote-with-builder
```

## CloudEvents subscriber

`cloudevents-subscriber` starts an HTTP sink for Kapro lifecycle CloudEvents and
registers a handler for successful promotions.

```bash
go run ./examples/06-sdk-go/01-cloudevents-subscriber
```

## Argo CD substrate

`argocd-substrate` registers and resolves the public Argo CD adapter through
the public SDK registry. It is a no-cluster proof that an external integration
can model Argo CD discovery without importing Kapro internals.

```bash
go run ./examples/06-sdk-go/02-argocd-substrate
```
