# Archive Examples

Long-term event archive examples, ordered from custom code to infrastructure
integrations.

| Folder | Topic |
|---|---|
| `00-go-subscriber/` | Minimal bespoke archive receiver |
| `01-eventing/` | Knative and Argo Events routing |
| `02-fluentbit/` | Fluent Bit log pipeline |
| `03-vector/` | Vector log pipeline |

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/09-archive/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/09-archive/00-go-subscriber/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
