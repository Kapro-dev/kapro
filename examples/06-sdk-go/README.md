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

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/06-sdk-go/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/06-sdk-go/00-promote-with-builder/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
