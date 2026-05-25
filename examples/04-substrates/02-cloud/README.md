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
