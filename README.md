# Kapro

**Kubernetes-native progressive delivery for multi-cluster fleets.**

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/kapro.io/kapro)](https://goreportcard.com/report/kapro.io/kapro)
[![API Group](https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple)](api/v1alpha1)

## Overview

Kapro is a progressive delivery engine that rolls out OCI artifacts across
Kubernetes cluster fleets in controlled waves. It uses label-based stage
selectors, composable gates, and a per-target state machine to deliver
safely to hundreds or thousands of clusters.

Kapro extends [Flux](https://fluxcd.io) with fleet-wide orchestration: CI
pushes an artifact, Kapro decides which clusters get it and in what order,
Flux handles the actual deployment on each cluster.

```
[ CI ] ──> [ Kapro ] ──> [ Flux ] ──> [ Cluster ]
            waves &        delivery
            gates
```

## Features

- **Progressive delivery** -- wave-based rollout via Pipeline stages with label selectors
- **Composable gates** -- soak, metrics (PromQL), approval, CEL, Job, webhook, cosign verification
- **Multi-cluster** -- one controller pod per spoke, outbound-only, no VPN or tunnel required
- **Multi-artifact** -- delta delivery of OCI bundles; only changed artifacts are pushed
- **Failure policies** -- halt, skip, or rollback per stage
- **Horizontal scaling** -- controller sharding for 1000+ cluster fleets
- **Full kubectl observability** -- all state is in CRDs, no external database

## Architecture

```
Hub cluster                          Spoke cluster
+------------------------+          +-----------------------------+
| kapro-system           |          | kapro-system                |
|  kapro-operator        |<---------+  kapro-cluster-controller   |
|  (5 controllers)       | outbound |  (polls hub, patches Flux)  |
|                        |   only   |                             |
| flux-system            |          | flux-system                 |
|  source-controller     |          |  source-controller          |
|  kustomize-controller  |          |  kustomize-controller       |
+------------------------+          +-----------------------------+
```

The hub runs the operator (Release, ReleaseTarget, Source, Approval, CSR
controllers). Each spoke runs a single-pod cluster-controller that reads
its `MemberCluster` spec from the hub, patches local Flux resources, and
reports health and convergence back.

## Custom Resource Definitions

| CRD | Scope | Description |
|-----|-------|-------------|
| `Artifact` | Cluster | Immutable OCI bundle reference, digest-pinned |
| `Pipeline` | Cluster | Reusable DAG of stages with label selectors (template, no controller) |
| `Release` | Cluster | Trigger for a rollout; references Artifacts and Pipelines |
| `ReleaseTarget` | Cluster | Per-target execution state (child of Release, controller-driven FSM) |
| `MemberCluster` | Cluster | Fleet cluster inventory with delivery config and health |
| `Approval` | Cluster | Human gate signal to unblock a waiting target |
| `Source` | Cluster | OCI registry watcher; auto-discovers semver tags |

## Getting Started

### Prerequisites

- Kubernetes 1.25+
- [Flux](https://fluxcd.io) installed on hub and spoke clusters
- Helm 3.x

### Install the hub operator

```bash
helm install kapro-operator charts/kapro-operator \
  -n kapro-system --create-namespace
```

### Register a spoke cluster

```bash
# On the hub: generate a bootstrap token
kapro cluster bootstrap --name prod-eu-01

# On the spoke: join the fleet
helm install kapro-cluster charts/kapro-cluster-controller \
  -n kapro-system --create-namespace \
  --set hub.kubeconfig=<path-to-hub-kubeconfig>
```

### Create a release

```yaml
apiVersion: kapro.io/v1alpha1
kind: MemberCluster
metadata:
  name: prod-eu-01
  labels:
    tier: prod
    region: eu-west
spec:
  actuator:
    type: flux
    flux:
      namespace: flux-system
      ociRepository: myapp
      kustomizationPath: ./overlays/prod
---
apiVersion: kapro.io/v1alpha1
kind: Pipeline
metadata:
  name: standard-rollout
spec:
  stages:
  - name: canary
    selector:
      matchLabels: { tier: canary }
  - name: prod
    selector:
      matchLabels: { tier: prod }
    dependsOn:
    - stage: canary
    gate:
      mode: manual
      gate:
        templates:
        - name: soak
          type: soak
          args:
            duration: 30m
---
apiVersion: kapro.io/v1alpha1
kind: Release
metadata:
  name: myapp-v1.2.4
spec:
  artifact: myapp-v1.2.4
  pipelines:
  - name: initial
    pipeline: standard-rollout
```

## Target FSM

Each target progresses through a 10-state finite state machine:

```
Pending -> Verification -> HealthCheck -> Soaking -> MetricsCheck
  -> WaitingApproval -> Applying -> Converged
                                 \-> Failed
                                 \-> Skipped
```

## Development

```bash
# Prerequisites: Go 1.22+, kind, make

# Generate CRDs and deepcopy
make generate manifests

# Run tests (unit + envtest)
make test

# Run operator locally against kind cluster
make run
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

## Roadmap

See [CHANGELOG.md](CHANGELOG.md) for the current release and
[docs/ROADMAP.md](docs/ROADMAP.md) for planned features.

## Community

- **Issues**: [GitHub Issues](https://github.com/vinnxcapital-gif/kapro/issues)
- **Discussions**: [GitHub Discussions](https://github.com/vinnxcapital-gif/kapro/discussions)

## License

Kapro is licensed under the [Apache License 2.0](LICENSE).

Kapro is a [Cloud Native Computing Foundation](https://cncf.io) project.
