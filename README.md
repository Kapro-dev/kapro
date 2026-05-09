# Kapro

**Progressive delivery engine for multi-cluster Kubernetes fleets.**

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/kapro.io/kapro)](https://goreportcard.com/report/kapro.io/kapro)
[![API Group](https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple)](api/v1alpha1)

## What is Kapro?

Kapro is the **canonical promotion layer** for Kubernetes. It decides
**when** and **where** a version is delivered across a fleet of clusters.
The actual deployment is delegated to pluggable actuators -- Kapro never
runs `helm upgrade` or `kubectl apply` on workload clusters directly.

Kapro solves fleet-wide progressive delivery for environments where:

- Clusters are behind NAT, firewalls, or air gaps (no inbound connectivity)
- Rollouts must move in ordered waves across countries, tiers, or regions
- Human approval and automated gates must block promotion between waves
- A single read model must answer "what version is running where, and why"

```
                    ┌──────────┐
[ CI ] ──> [ OCI ] ──>│  Kapro   │──> [ Actuator Plugin ] ──> [ Cluster ]
                    │ (decides) │    (delivers)
                    └──────────┘
```

Kapro does not own or replace your delivery system. It orchestrates it.

## Architecture

```
Hub cluster                              Spoke cluster
┌────────────────────────────┐           ┌─────────────────────────────┐
│ kapro-system               │           │ kapro-system                │
│                            │           │                             │
│  kapro-operator            │  outbound │  kapro-cluster-controller   │
│  ├── ReleaseReconciler     │◄──────────┤  (single pod, polls hub)    │
│  ├── ReleaseTargetReconciler│   only   │                             │
│  ├── SourceReconciler      │           │  Reads MemberCluster spec   │
│  ├── ApprovalReconciler    │           │  Patches local delivery CRDs│
│  └── CSRApprovalReconciler │           │  Reports health + versions  │
│                            │           │                             │
│  webhook-server            │           │  Your delivery system:      │
│  ├── /approve              │           │  Your delivery system       │
│  ├── /reject               │           │  (any KAI-compatible tool)  │
│  └── /status               │           │                             │
└────────────────────────────┘           └─────────────────────────────┘
```

**Hub never dials spokes.** Each spoke runs a single-pod cluster-controller
that posts outbound heartbeats and polls for desired state. This works
across NAT, firewalls, VPNs, and air-gapped networks with no tunnel or
service mesh required.

## Custom Resource Definitions

| CRD | Description |
|-----|-------------|
| **Artifact** | Immutable OCI bundle reference, digest-pinned. Created by CI or auto-discovered. |
| **Pipeline** | Reusable DAG of stages with label selectors. Pure template -- no controller. |
| **Release** | Trigger for a rollout. References Artifacts and Pipelines. |
| **ReleaseTarget** | Per-target execution state. Child of Release, driven by a 10-state FSM. |
| **MemberCluster** | Fleet cluster registry. Delivery config, health, heartbeat, capabilities. |
| **Approval** | Human gate signal. Kubectl-native, auditable, supports P0 bypass. |
| **Source** | OCI registry watcher. Auto-discovers semver tags and creates Artifacts. |

All CRDs are cluster-scoped. All state lives in Kubernetes etcd --
no external database, no message bus, no separate registry.

## Key Concepts

### Pluggable Actuators (KAI Interface)

Kapro does not hardcode how deployments reach clusters. The Kapro Actuator
Interface (KAI) is a pluggable contract:

| Actuator | What it does |
|----------|-------------|
| **OCI** (built-in) | Patches `MemberCluster.spec.desiredVersions`; cluster-controller patches local OCI sources |
| **ArgoCD** (planned) | Patches `Application.spec.source.targetRevision` |
| **Sveltos** (planned) | Patches `ClusterProfile.spec.helmCharts` |
| **Custom** | Implement the KAI interface for any delivery system |

### Composable Gates

Gates block promotion between stages until conditions are met:

| Gate | Description |
|------|-------------|
| **Soak** | Minimum time elapsed before advancing |
| **Metrics** | PromQL evaluation with configurable threshold |
| **Approval** | Blocks until an Approval CR exists |
| **CEL** | Inline expression evaluation with target context |
| **Job** | Kubernetes Job; exit 0 = pass |
| **Webhook** | HTTP callback to external system |
| **Verification** | Cosign signature verification (keyless OIDC + static key) |
| **Health** | Active endpoint polling |

### Stage Failure Policies

| Policy | Behavior |
|--------|----------|
| **halt** (default) | Cancel all in-flight siblings, fail the Release |
| **skip** | Mark failed targets as Skipped, continue the stage |
| **rollback** | Revert all converged targets in the stage and prior stages |

### Target State Machine

Each target progresses independently through a 10-state FSM:

```
Pending -> Verification -> HealthCheck -> Soaking -> MetricsCheck
        -> WaitingApproval -> Applying -> Converged
                                       |-> Failed
                                       |-> Skipped
```

### Wave-Based Rollout

Pipeline stages select targets via label selectors. Adding a cluster
to a wave is a label change -- no Pipeline modification required:

```yaml
stages:
- name: pilot
  selector: { matchLabels: { wave: pilot } }
- name: eu-west
  selector: { matchLabels: { region: eu-west } }
  dependsOn: [{ stage: pilot }]
  gate:
    mode: manual
    gate:
      templates:
      - { name: soak, type: soak, args: { duration: 1h } }
- name: all-prod
  selector: { matchLabels: { tier: prod } }
  dependsOn: [{ stage: eu-west }]
```

### Scalability

- **Controller sharding** via `KAPRO_SHARD` env + label predicate for 1000+ cluster fleets
- **Lease-based heartbeat** using `coordination.k8s.io/v1` instead of status writes
- **Field-indexed lookups** for O(1) release-to-target queries
- **Conditional poll** -- reconciler skips cycles when no work is pending

## Getting Started

### Prerequisites

- Kubernetes 1.25+
- Helm 3.x
- A KAI-compatible delivery system on each spoke cluster

### Install the hub operator

```bash
helm install kapro-operator charts/kapro-operator \
  -n kapro-system --create-namespace
```

### Register a spoke cluster

```bash
# On the hub: create a bootstrap token
kapro cluster bootstrap --name prod-eu-01

# On the spoke: join the fleet
kapro cluster join --hub-kubeconfig <path> --name prod-eu-01 \
  --labels tier=prod,region=eu-west,wave=pilot
```

### Create a release

```yaml
apiVersion: kapro.io/v1alpha1
kind: Release
metadata:
  name: platform-v1.2.4
spec:
  artifact: platform-v1.2.4
  pipelines:
  - name: eu-rollout
    pipeline: eu-fleet
```

### Observe the rollout

```bash
kapro get releases
kapro get targets --release platform-v1.2.4

# Or native kubectl:
kubectl get releases
kubectl get releasetargets -l kapro.io/release=platform-v1.2.4
```

## Development

```bash
# Prerequisites: Go 1.22+, kind, make
make generate manifests   # CRDs + deepcopy
make test                 # unit + envtest
make run                  # operator against kind cluster
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## Documentation

- [CHANGELOG.md](CHANGELOG.md) -- current release notes
- [docs/ROADMAP.md](docs/ROADMAP.md) -- planned features
- [docs/SPEC.md](docs/SPEC.md) -- API specification

## License

Apache 2.0 -- see [LICENSE](LICENSE).
