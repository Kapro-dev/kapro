# CloudEvents Subscriber

Minimal Go SDK example for receiving Kapro lifecycle CloudEvents.

```text
Kapro event -> HTTP subscriber -> handler
```

Run locally:

```bash
go run ./examples/06-sdk-go/01-cloudevents-subscriber
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/06-sdk-go/01-cloudevents-subscriber/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/06-sdk-go/01-cloudevents-subscriber/run.sh test
examples/06-sdk-go/01-cloudevents-subscriber/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
