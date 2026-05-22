# Custom Operator

This example builds a Kapro operator that registers an in-process Go gate named
`business-hours`.

```bash
go build ./examples/sdk-go/custom-operator
```

Build this package into your own container image and set the Kapro chart image
override to that image. Plans can then use:

```yaml
gate:
  mode: auto
  gate:
    templates:
      - name: business-hours
        type: business-hours
```
