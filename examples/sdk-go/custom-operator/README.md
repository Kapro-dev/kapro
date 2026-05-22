# Custom Operator

This example builds a Kapro operator that registers an in-process Go gate named
`business-hours`.

```bash
go build ./examples/sdk-go/custom-operator
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
