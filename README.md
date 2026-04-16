# 🦘 Kapro

> **The Canonical Promotion Layer for Kubernetes.**
> Passes versions forward. Through environments. Across countries. In waves.

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![API Group](https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple)](api/v1alpha1)

---

## What is Kapro?

Kapro is a **Flux-native, OCI-first, multi-cluster progressive delivery engine** for country-scale Kubernetes platforms.

It sits between [Kargo](https://kargo.io) (pre-prod promotion) and [Flux](https://fluxcd.io) (deployment) — the missing horizontal wave layer neither provides.

```
[ CI ] ──▶ [ Kargo (optional) ] ──▶ [ Kapro ] ──▶ [ Flux ] ──▶ [ Cluster ]
              pre-prod stages          promotion      delivery
                                       + waves
```

---

## The Three Layers

| Layer | Concern | CRDs |
|---|---|---|
| **ARTIFACT** | what travels | `Artifact` |
| **TOPOLOGY** | where it goes | `Environment`, `EnvironmentGroup`, `ClusterRegistration` |
| **STRATEGY** | how it moves | `PromotionPolicy`, `Pipeline`, `Release`, `Approval` |

---

## Key Features

- **8 CRDs** — clean, composable API across 3 layers
- **CRD-native cluster connectivity** — one controller pod per cluster, outbound only, no gRPC
- **DAG batch progression** — wave-based rollout across 33+ countries
- **Manual + auto promotion** — soak time, metrics gates, human approval
- **Flux actuator** — mutates OCI tag, Flux delivers
- **Full `kubectl` observability** — no custom tooling needed

---

## Architecture

```
Control plane cluster              Workload cluster
┌──────────────────────┐           ┌─────────────────────────────┐
│ kapro-system         │           │ kapro-system                │
│  kapro-operator      │◀──────────│  kapro-cluster-controller   │
│  (8 CRD controllers) │ CRD write │  Deployment, 1 pod          │
│                      │           │  NOT a DaemonSet            │
│ flux-system          │           │ flux-system                 │
│  source-controller   │           │  source-controller          │
│  kustomize-ctrl      │           │  kustomize-controller       │
└──────────────────────┘           └─────────────────────────────┘
```

---

## Quick Start

```bash
# Install control plane
helm install kapro-operator charts/kapro-operator -n kapro-system --create-namespace

# Register a workload cluster
helm install kapro-cluster charts/kapro-cluster-controller -n kapro-system \
  --set controlPlane.url=https://kapro.example.com \
  --set environment=de-prod
```

---

## API Example

```yaml
apiVersion: kapro.io/v1alpha1
kind: Release
metadata:
  name: ocs-v1.2.4
spec:
  artifact: ocs-v1.2.4
  scope:
    selector:
      matchLabels:
        country: de
  pipelineRef: standard-global-rollout
```

---

## Development

```bash
# Prerequisites: Go 1.22+, Node 20+, controller-gen, helm

# Backend
go mod tidy
make generate   # generate CRD manifests + DeepCopy
make run        # run operator locally

# Frontend
cd ui && npm install && npm run dev
```

---

## License

Apache 2.0 — see [LICENSE](LICENSE)

---

*Kapro mascot: 🦘 the kangaroo — carries versions in its pouch, hops forward in stages.*
