# Custom Substrates

A substrate is a delivery domain. An actuator is the program that performs
delivery for that domain.

Kapro ships built-in substrates for Argo CD, Flux, and OCI. The same API also
allows a platform team to register a custom substrate without changing Kapro's
CRDs.

```yaml
apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: hello-world
spec:
  substrate:
    kind: hello-world
    actuator: hello-world
  execution:
    mode: hub-push
  parameters:
    message: hello from kapro
```

## Minimal Go Actuator

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

- [`examples/actuator-hello-world/`](https://github.com/Kapro-dev/kapro/tree/main/examples/actuator-hello-world)
- README: walk-through of `substrate.kind`, `substrate.actuator`,
  `execution.mode`, and `BoolFunc`.
- `hello_test.go`: registers the substrate via the public registry path,
  asserts `Apply` succeeds, and pins the capability profile so changes are
  deliberate.
- Verified on every PR via `make conformance-hello-world` in the Conformance
  CI job — if a change to `pkg/kapro/actuator` breaks the authoring shape
  used by hello-world, CI fails before merge.
