# Cloud Examples

This directory contains optional provider-specific onboarding helpers.

Kapro's core control plane is cloud-neutral and works with generic Kubernetes,
Argo CD, Flux, and external plugins. Cloud examples belong here when they help
teams bootstrap or register clusters on a specific provider without making that
provider part of the core API.

- `00-gcp/` contains GKE and Google Workload Identity helper scripts.

Run the GCP helper help from the repository root:

```bash
./examples/04-substrates/02-cloud/00-gcp/register-spoke.sh --help
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/04-substrates/02-cloud/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/04-substrates/02-cloud/00-gcp/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
