# Programmable Gates

This example registers two in-process gate types:

- `canary-error-rate`
- `external-readiness`

They are ordinary Go functions adapted with `gate.Func`. Use this pattern when
the gate logic belongs in the same trust boundary as the operator.

```text
Go function -> gate.Func adapter -> programmable gate
```

Run from the repository root:

```bash
go run ./examples/06-sdk-go/03-programmable-gates
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/06-sdk-go/03-programmable-gates/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/06-sdk-go/03-programmable-gates/run.sh test
examples/06-sdk-go/03-programmable-gates/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
