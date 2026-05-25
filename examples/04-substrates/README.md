# Substrates

Examples for substrate configuration and adoption.

| Folder | Topic |
|---|---|
| `00-class-config/` | SubstrateClass-backed configs |
| `01-existing-gitops/` | Existing Argo CD and Flux topology discovery |
| `02-cloud/` | Cloud provider onboarding helpers |
| `03-clustertemplate/` | ClusterTemplate example |

Validate substrate examples with:

```bash
scripts/validate-yaml-json
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/04-substrates/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/04-substrates/00-class-config/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
