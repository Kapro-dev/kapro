# Argo CD Substrate SDK Proof

This example shows the public Go SDK surface an out-of-tree Argo CD substrate
integration can import without depending on Kapro internals.

It registers the public Argo CD reference adapter, resolves it through the
public adapter registry, and prints the modeled discovery shape for Argo CD
Applications and ApplicationSets.

## Run

```bash
go run ./examples/06-sdk-go/02-argocd-substrate
```

The example is local-only and does not require a Kubernetes cluster.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/06-sdk-go/02-argocd-substrate/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/06-sdk-go/02-argocd-substrate/run.sh test
examples/06-sdk-go/02-argocd-substrate/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
