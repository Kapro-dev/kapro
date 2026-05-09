# GKE Setup Guide

This guide covers setting up Kapro with Flux Operator on GKE clusters.

## Architecture

```
GKE Hub cluster (management)
├── kapro-operator          (promotion brain)
├── flux-operator           (manages Flux lifecycle + ResourceSets)
└── Flux                    (syncs config to spoke clusters)

GKE Spoke clusters (workload)
├── Flux                    (installed by Flux Operator)
└── (no kapro component)
```

## Prerequisites

- GKE clusters with Workload Identity enabled
- Google Artifact Registry (GAR) for OCI artifacts
- gcloud, kubectl, helm CLI tools

## Quick Start

### 1. Install Flux Operator + Kapro on Hub

```bash
helm install flux-operator oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator \
  -n flux-system --create-namespace

helm install kapro-operator charts/kapro-operator \
  -n kapro-system --create-namespace
```

### 2. Register Spoke Clusters

```bash
# Create kubeconfig Secret on hub for each spoke
kubectl create secret generic de-prod-kubeconfig \
  --from-file=value=<spoke-kubeconfig> -n flux-system

# Register with Kapro
kapro cluster bootstrap --name de-prod \
  --labels tier=prod,country=de,region=europe-west3
```

### 3. Create ResourceSet + Pipeline + Release

See examples/ directory for complete YAML.

### 4. Monitor and Approve

```bash
kapro get releases
kapro get targets
kapro approve <release>/<target>
kapro fleet
```

## Workload Identity

Bind Flux SA to GCP SA for GAR access without static credentials:

```bash
gcloud iam service-accounts create flux-gar-reader --project=PROJECT
gcloud artifacts repositories add-iam-policy-binding kapro-bundles \
  --location=REGION --project=PROJECT \
  --member="serviceAccount:flux-gar-reader@PROJECT.iam.gserviceaccount.com" \
  --role="roles/artifactregistry.reader"
```
