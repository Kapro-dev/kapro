# Cloud Examples

This directory contains optional provider-specific onboarding helpers.

Kapro's core control plane is cloud-neutral and works with generic Kubernetes,
Argo CD, Flux, and external plugins. Cloud examples belong here when they help
teams bootstrap or register clusters on a specific provider without making that
provider part of the core API.

- `gcp/` contains GKE and Google Workload Identity helper scripts.
- `classifier-vault-bootstrap-preview.yaml` shows the v0.2.3 preview API
  shape for ClusterClassifier staging hints and Vault bootstrap material. The
  built-in controller fails closed for the Vault branch unless external
  automation publishes the material.
