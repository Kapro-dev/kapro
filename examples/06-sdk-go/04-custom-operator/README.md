# Custom Operator

This example builds a Kapro operator that registers an in-process Go gate named
`business-hours`.

```bash
go build ./examples/06-sdk-go/04-custom-operator
```

Build this package into your own container image and set the Kapro chart image
override to that image.

Until programmable in-process gate types graduate the CRD enum, reference an
in-process gate from a Plan via `type: plugin`. The Target reconciler looks up
`plugin.name` against the in-process gate registry first and falls back to the
gRPC plugin gateway, so a registered Go gate resolves without leaving the
operator process.

```yaml
gate:
  mode: auto
  gate:
    templates:
      - name: business-hours
        type: plugin
        plugin:
          name: business-hours
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/06-sdk-go/04-custom-operator/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/06-sdk-go/04-custom-operator/run.sh test
examples/06-sdk-go/04-custom-operator/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
