# GCP Spoke Registration

Helper script for registering GCP spoke clusters.

```text
GCP project + cluster -> register-spoke.sh -> Kapro Cluster input
```

Run from the repository root after reading `docs/extending/cloud-gcp.md`:

```bash
./examples/04-substrates/02-cloud/00-gcp/register-spoke.sh --help
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/04-substrates/02-cloud/00-gcp/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
