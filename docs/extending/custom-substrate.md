# Custom Substrates

A substrate is a delivery domain. A `SubstrateClass` names the implementation
contract, a typed config CRD stores platform wiring, and a substrate
implementation performs delivery for that class.

Kapro ships built-in substrates for Argo CD, Flux, OCI, and Kubernetes direct
apply. The same API lets a platform team register a
custom substrate without changing Kapro core CRDs.

```yaml
apiVersion: kapro.io/v1alpha1
kind: SubstrateClass
metadata:
  name: hello-world
spec:
  controllerName: example.com/hello-world
  executionModes:
    default: hub-push
---
apiVersion: example.com/v1alpha1
kind: HelloWorldConfig
metadata:
  name: hello-world
spec:
  message: hello from kapro
---
apiVersion: kapro.io/v1alpha1
kind: Substrate
metadata:
  name: hello-world
spec:
  classRef:
    name: hello-world
  configRef:
    apiVersion: example.com/v1alpha1
    kind: HelloWorldConfig
    name: hello-world
  execution:
    mode: hub-push
```

The substrate's controller owns `SubstrateClass.status` for
`controllerName=example.com/hello-world` and reports accepted config kinds,
supported execution modes, and capabilities.

## KSI Contract

New substrate packages should implement KSI, the Kapro Substrate Interface, at
`kapro.io/kapro/pkg/kapro/substrate`:

```go
type Substrate interface {
    Validate(ctx context.Context, req *ValidateRequest) (*ValidateResult, error)
    Apply(ctx context.Context, req *ApplyRequest) (*ApplyResult, error)
    Observe(ctx context.Context, req *ObserveRequest) (*ObserveResult, error)
    Capabilities(ctx context.Context) (*Capabilities, error)
}
```

KSI requests carry the resolved `SubstrateClass`, `Substrate`, typed config
object, target `Cluster`, desired versions, and compatibility parameters. Use a
typed config CRD for durable parameters; keep string maps only for demos or
migration.

## Minimal Go Actuator Compatibility

`BoolFunc` is the smallest possible actuator shape. It is useful for examples,
tests, and very small internal checks:

```go
server.Actuators.Register("hello-world", actuator.NewBoolFunc(
    "hello-world",
    func(ctx context.Context, req actuator.ApplyRequest) (bool, string, error) {
        return true, "hello world delivered", nil
    },
))
```

`true` means the apply/observe operation succeeded. `false` fails the operation
with the returned message. A non-nil error fails the operation with an
`ApplyError` wrapper.

Production actuators should implement the full `actuator.Actuator` interface
and publish explicit capabilities. That makes rollback, observe, dry-run, and
two-phase behavior visible to Kapro and to conformance tests.

## External Plugin Path

Use the gRPC KAI contract when the actuator should ship or scale independently
from the Kapro operator. The public service includes capability discovery,
apply, observe/convergence, and rollback methods. See
[Actuator Plugin Contract](actuator-plugin-contract.md).

## Working example

A live, CI-verified hello-world substrate ships in the repo:

- [`examples/07-actuator-hello-world/`](https://github.com/Kapro-dev/kapro/tree/main/examples/07-actuator-hello-world)
- README: walk-through of `substrate.kind`, `substrate.actuator`,
  `execution.mode`, and `BoolFunc`.
- `hello_test.go`: registers the substrate via the public registry path,
  asserts `Apply` succeeds, and pins the capability profile so changes are
  deliberate.
- Verified on every PR via `make conformance-hello-world` in the Conformance
  CI job — if a change to `pkg/kapro/actuator` breaks the authoring shape
  used by hello-world, CI fails before merge.
