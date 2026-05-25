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
