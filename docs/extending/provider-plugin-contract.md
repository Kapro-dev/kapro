# Provider Plugin Contract

KSP is the Kapro Spoke Provider contract. It runs inside
`kapro-cluster-controller` and reconciles one app/version tuple on the local
spoke cluster.

KSP is different from cloud discovery providers:

- KSP providers perform spoke-side delivery or observation for a substrate
  kind selected by `Substrate.spec.substrate.kind` or `Substrate.spec.classRef`.
- Cloud discovery providers enumerate clusters for `ClusterTemplate` import.

Keep those axes separate. Delivery providers must not become broad fleet
inventory plugins.

## Version

The current KSP contract version is `v1alpha1`, exposed as
`spokeprovider.ContractVersionV1Alpha1`.

Provider implementations expose metadata through `Capabilities()`:

```go
type Provider interface {
    SubstrateKind() kaprov1alpha1.SubstrateKind
    Capabilities() spokeprovider.Capabilities
    Reconcile(context.Context, spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult
}
```

## Capabilities

| Capability | Meaning |
|---|---|
| `reconcile` | Provider can handle one `(cluster, app, desiredVersion)` tick. |
| `observe` | Provider can observe backend state without requiring mutation. |
| `apply` | Provider can apply desired state on the spoke. |
| `dry-run` | Provider can validate without persisting changes. |

OCI advertises reconcile, observe, and apply. Flux currently advertises
reconcile and observe because the spoke provider is read-only; mutation is owned
by the hub-side Flux actuator.

## Registration

Legacy registration remains valid:

```go
reg := spokeprovider.NewRegistry()
_ = reg.Register(kaprov1alpha1.SubstrateKindOCI, provider)
```

New providers should register explicit metadata:

```go
_ = reg.RegisterRegistration(spokeprovider.Registration{
    SubstrateKind: kaprov1alpha1.SubstrateKindExternal,
    Capabilities: spokeprovider.Capabilities{
        SubstrateKind: kaprov1alpha1.SubstrateKindExternal,
        SupportsReconcile: true,
        SupportsObserve: true,
    },
    Provider: provider,
})
```

## Conformance

Provider authors can use the Go conformance harness:

```go
func TestKSPConformance(t *testing.T) {
    providerconformance.Run(t, myProvider, providerconformance.DefaultScenario())
}
```

The harness checks that capability metadata is populated, the contract version
is supported, `Reconcile` never panics, and repeated reconciles for the same
request produce a stable result shape.

The CLI can run the same suite against Kapro's reference provider:

```bash
go run ./cmd/kapro-conformance provider
go run ./cmd/kapro-conformance provider -o json
```

Custom KSP providers should keep using the Go harness from their own tests
because KSP is an in-process SDK contract, not a live gRPC endpoint.
