# Hello-World Custom Substrate

The smallest possible Kapro substrate. Proves the open-substrate API in CI on
every PR so the "you can plug in your own delivery technology" claim doesn't
bit-rot.

Backed by `actuator.NewBoolFunc`. Real production substrates implement the
full `actuator.Actuator` interface with explicit capability bits; this example
exists to show the contract and to anchor the CI guarantee.

## Concepts

| Field | Plain meaning |
|---|---|
| `spec.substrate.kind` | The delivery domain. Open string. Built-ins: `argo`, `flux`, `oci`, `kubernetes-apply`. Custom: anything that matches `^[a-z][a-z0-9-]{0,62}$`. |
| `spec.substrate.actuator` | The concrete implementation registered under the kind. Optional for built-ins (defaulted from kind); set explicitly for custom substrates like this one. |
| `spec.execution.mode` | Where Kapro runs the actuator. `hub-push` for in-process Go actuators like this one; `spoke-pull` for cluster-agent-driven delivery; `external-pull` for systems that consume Kapro decisions out-of-band. |
| `BoolFunc` | Sugar wrapper that adapts a `(ctx, req) -> (bool, string, error)` function to the full `actuator.Actuator` interface. Use for trivial substrates and tests. |

See [`docs/concepts/api-naming.md`](../../docs/concepts/api-naming.md) for the
full API naming guide.

## Build and run

```bash
cd examples/07-actuator-hello-world
go build -o hello-world .
./hello-world
```

The binary registers a `hello-world` substrate and calls `Apply` against a
synthetic request. Production usage would wire the substrate into
`server.Actuators` via the embedded Kapro operator (see
[`docs/extending/sdk-server.md`](../../docs/extending/sdk-server.md)).

## Apply against a cluster

```bash
kubectl apply -f substrate.yaml
kubectl get substrate hello-world -o yaml
```

After a promotion targets a cluster whose Substrate points at this substrate,
inspect the decision trail:

```bash
kapro why <promotion-name>
```

You should see the Apply event sourced from the hello-world substrate.

## CI guarantee

Every Kapro PR runs:

```bash
make conformance-hello-world
```

That target builds this example, runs its tests, and invokes
`kapro-conformance` against the registered substrate. A change in the public
`pkg/kapro/actuator` SDK that breaks the authoring shape used here fails CI
before it merges.

## Production warning

`BoolFunc` is sugar. It returns `Capabilities{SupportsRollback: false}` because
trivial actuators can't unwind side effects, and it implements `IsConverged`
trivially. Production substrates should:

1. Implement `actuator.Actuator` directly (not via `BoolFunc`).
2. Declare every capability bit explicitly so the controller can route around
   missing capabilities cleanly.
3. Handle Apply/Observe/Rollback semantics for real, with substrate-specific
   retry, dry-run, and convergence logic.

Use this example to learn the contract. Don't copy it into production.

## What's next

- [`docs/extending/custom-substrate.md`](../../docs/extending/custom-substrate.md) — full custom-substrate authoring guide
- [`docs/extending/actuator-plugin-contract.md`](../../docs/extending/actuator-plugin-contract.md) — gRPC plugin contract for out-of-process substrates
- [`docs/concepts/api-naming.md`](../../docs/concepts/api-naming.md) — the substrate/execution naming guide

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/07-actuator-hello-world/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/07-actuator-hello-world/run.sh test
examples/07-actuator-hello-world/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

```bash
kubectl delete -f examples/07-actuator-hello-world --ignore-not-found
```
