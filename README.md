# Kapro

**Progressive delivery engine for multi-cluster Kubernetes fleets.**

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/kapro.io/kapro)](https://goreportcard.com/report/kapro.io/kapro)
[![API Group](https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple)](api/v1alpha1)

## What is Kapro?

Kapro orchestrates **when** and **where** versions are delivered across a
fleet of Kubernetes clusters. It doesn't replace Flux or ArgoCD -- it
orchestrates them across clusters in ordered waves with approval gates.

Built for environments where:

- Clusters are behind NAT, firewalls, or air gaps (no inbound connectivity)
- Rollouts must move in ordered waves across countries, tiers, or regions
- Human approval and automated gates must block promotion between waves
- Per-cluster k9s visibility is required (not just hub-side status)

```
CI --> kapro bundle push --> GAR (OCI)
                              |
Hub: Release CR --> Pipeline DAG --> spoke actuator
                              |
Spoke: OCIRepository --> wave Kustomizations --> HelmReleases (local)
```

## Architecture

```
Hub cluster (GKE)                        Spoke cluster (GKE/SKE/any)
+----------------------------+           +-----------------------------+
| kapro-operator             |           | Flux (spoke-local)          |
|   Release controller       |           |   source-controller         |
|   ReleaseTarget FSM        |           |   kustomize-controller      |
|   Approval controller      |           |   helm-controller           |
|   Kapro controller         |           |                             |
|     (spoke bootstrap)      | kubeconfig|   OCIRepository             |
|     (bundle push)          |---------->|     pulls bundle from GAR   |
|                            |           |                             |
| Flux Operator              |           |   wave-00 Kustomization     |
|   FluxInstance             |           |   wave-01 Kustomization     |
|   ResourceSet (push mode)  |           |     dependsOn: wave-00      |
|                            |           |   wave-02 Kustomization     |
| MemberCluster CRDs         |           |     dependsOn: wave-01      |
|   (fleet inventory)       |           |                             |
+----------------------------+           |   HelmReleases (local)      |
                                         |     hello-infra (wave 0)    |
GCP Services                             |     hello-app (wave 1)      |
+----------------------------+           |     hello-frontend (wave 2)  |
| Fleet API (membership)     |           |                             |
| GAR (OCI bundles + charts) |           |   Observability             |
| Secret Manager (ESO)       |           |     Beyla -> OTel -> GMP    |
| Cloud SQL (PostgreSQL)     |           +-----------------------------+
| Workload Identity (auth)   |
| GMP (Prometheus metrics)   |
+----------------------------+
```

## Delivery Modes

| Mode | How it works | Visibility |
|------|-------------|------------|
| **spoke** (recommended) | OCI bundle pulled by spoke's own Flux. Wave Kustomizations with dependsOn chain. | Full k9s on spoke: `kubectl get ks,hr` |
| **push** | Hub renders HelmReleases with kubeConfig, hub's helm-controller installs remotely. | Hub only: spoke sees pods but no Flux resources |

Set via `deliveryMode: spoke` on the Kapro CR.

## CRDs (8)

| CRD | Description |
|-----|-------------|
| **Kapro** | Fleet entry point. References KaproApp, defines clusters and pipeline. |
| **KaproApp** | Component definitions: registries, waves, dependsOn, defaults, overrides. |
| **Pipeline** | Reusable DAG of stages with label selectors. Pure template. |
| **Release** | Trigger for rollout. References version and pipelines. |
| **ReleaseTarget** | Per-target FSM state (child of Release). |
| **MemberCluster** | Fleet cluster registry: actuator config, health, version, heartbeat. |
| **Approval** | Human gate signal. kubectl-native, auditable, supports bypass. |
| **AgentPolicy** | AI trust boundaries for automated approvals. |

## CLI (`kapro`)

```
kapro hub init --project X --cluster Y     # bootstrap hub (Flux + CRDs + Fleet)
kapro hub registry create --project X      # create centralized GAR registry
kapro hub registry add <url> --as default  # save registry to config
kapro spoke add <name> --provider gcp-fleet # add spoke (Flux + Fleet + IAM)
kapro fleet list                           # list Fleet memberships
kapro fleet sync --project X               # auto-discover + add all Fleet clusters
kapro bundle generate --app X --push       # generate + push OCI bundle (CI)
kapro status                               # live fleet delivery dashboard
kapro approve <release>/<target>           # approve gate
kapro reject <release>/<target>            # reject gate
kapro rollback <release> --to <version>    # rollback
```

All commands use Go SDK (Fleet Hub, Container, IAM, Artifact Registry, ORAS).
No gcloud, helm, flux, or kubectl dependencies. Interactive project/cluster
selection when no flags provided.

Config persisted at `~/.kapro/config.yaml` -- hub init writes it, all commands read it.

## Getting Started

```bash
# 1. Bootstrap hub cluster
kapro hub init --project my-project --cluster my-hub

# 2. Create registry
kapro hub registry create --project my-project --name kapro-registry
kapro hub registry add oci://europe-west1-docker.pkg.dev/my-project/kapro-registry --as default

# 3. Add spoke clusters
kapro spoke add de-prod-01 --provider gcp-fleet --labels tier=canary
kapro spoke add fi-prod-01 --provider gcp-fleet --labels tier=prod

# 4. Define app + delivery
kubectl apply -f kaproapp.yaml   # components, waves, registries
kubectl apply -f kapro.yaml      # fleet config, deliveryMode: spoke

# 5. CI pushes bundle
kapro bundle generate --app my-app --name my-kapro --version 1.0.0 --push

# 6. Create release (triggers delivery)
kubectl apply -f release.yaml
```

## Composable Gates

| Gate | Description |
|------|-------------|
| **Soak** | Minimum time elapsed before advancing |
| **Metrics** | PromQL evaluation with configurable threshold |
| **Approval** | Blocks until Approval CR exists |
| **CEL** | Inline expression evaluation with target context |
| **Job** | Kubernetes Job; exit 0 = pass |
| **Webhook** | HTTP callback to external system |
| **Verification** | Cosign signature verification |
| **Health** | Active endpoint polling |

## Target State Machine

```
Pending -> Verification -> HealthCheck -> Soaking -> MetricsCheck
        -> WaitingApproval -> Applying -> Converged
                                       |-> Failed
                                       |-> Skipped
```

## GCP-Native Integration

| GCP Service | How Kapro uses it |
|-------------|-------------------|
| **GKE Fleet** | Cluster discovery, membership management |
| **Artifact Registry** | Centralized OCI bundles + Helm charts |
| **Container API** | Cluster endpoint resolution, location detection |
| **Secret Manager** | Credentials via External Secrets Operator |
| **Cloud SQL** | Managed PostgreSQL for platform services |
| **Workload Identity** | Zero-credential auth (pod -> GCP SA) |
| **GMP** | Prometheus metrics for health gates |
| **IAM** | Cross-project spoke access to hub registry |

All via Go SDK. Also supports non-GCP clusters via `--kubeconfig` provider
(StackIT SKE, EKS, AKS, on-prem, kind).

## Scale

- Parallel spoke bootstrap (10 concurrent, bounded semaphore)
- Spoke client cache (sync.Map, 5min TTL, health probe on reuse)
- Version-change detection (skip redundant bootstrap)
- Error isolation (failing spoke doesn't block fleet)
- Controller sharding via `KAPRO_SHARD` env for 1000+ clusters
- Embedded CRDs via go:embed (FluxInstance + ResourceSet + 8 Kapro CRDs)

## Development

```bash
# Prerequisites: Go 1.22+, make
make generate manifests   # CRDs + deepcopy
go build ./...            # all packages
go test ./...             # tests

# Run operator locally against GKE hub
KAPRO_DEV_MODE=1 KAPRO_DISABLE_WEBHOOKS=true go run ./cmd/operator/
```

## License

Apache 2.0 -- see [LICENSE](LICENSE).
