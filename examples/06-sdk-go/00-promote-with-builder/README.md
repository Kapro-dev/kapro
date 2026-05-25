# Promote With Builder

Smallest Go SDK example for constructing a `Promotion` with the fluent builder.

```text
Go builder -> Promotion object -> fake client
```

Run without a Kubernetes cluster:

```bash
go run ./examples/06-sdk-go/00-promote-with-builder
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/06-sdk-go/00-promote-with-builder/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/06-sdk-go/00-promote-with-builder/run.sh test
examples/06-sdk-go/00-promote-with-builder/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
