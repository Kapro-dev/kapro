<p align="center">
  <img src="docs/logo.png" alt="Kapro logo" width="300">
</p>

<h1 align="center">Kapro</h1>

<p align="center"><strong>The canonical promotion layer for Kubernetes.</strong><br>
Purpose-built for sovereign fleet GitOps at global scale.</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
  <a href="https://goreportcard.com/report/kapro.io/kapro"><img src="https://goreportcard.com/badge/kapro.io/kapro" alt="Go Report Card"></a>
  <a href="api/v1alpha1"><img src="https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple" alt="API Group"></a>
</p>

---

## The Problem

Modern retail, financial services, and distributed enterprises face a critical challenge: **how do you safely deploy to hundreds or thousands of Kubernetes clusters across multiple countries without creating coordination bottlenecks or blast radius explosions?**

Traditional GitOps approaches break down at sovereign fleet scale:

- **Sequential pipelines don't scale** — coordinating 100+ clusters through linear CI/CD creates intractable dependency chains
- **No blast radius control** — a bad deployment can cascade across entire fleets before detection
- **Missing promotion semantics** — Flux and ArgoCD lack native concepts for "deploy to 3 test clusters, then 10% of production, then all"
- **Coordination overhead** — manual promotion gates and spreadsheet-driven rollout tracking don't scale

<p align="center">
  <img src="docs/fleet-scale.png" alt="The fleet-scale imperative" width="700">
</p>

## The Generation Change

We are no longer deploying simple containers. Modern platforms orchestrate stateful distributed systems — with cross-platform dependencies, ordered deployment waves, and strict reconciliation loops — across an untrusted fleet.

<p align="center">
  <img src="docs/generation-change.png" alt="The architectural generation change" width="700">
</p>

## Why Sequential Pipelines Break

Traditional CI/CD assumes a linear world: build, test, deploy. But when Kafka must run before 14 dependent services, databases need managed state, and clusters must self-correct drift — sequential pipelines simply cannot express this.

<p align="center">
  <img src="docs/sequential-pipelines-break.png" alt="Why sequential pipelines break" width="700">
</p>

## The Artifact is the Contract

Kapro decouples CI from deployment. The OCI artifact becomes the single source of truth — immutable, signed, and version-locked. Any git repo, any CI pipeline can produce it. Runtime git dependency drops to zero.

<p align="center">
  <img src="docs/artifact-contract.png" alt="Multi-pipeline GitOps: the artifact is the contract" width="700">
</p>

| | Traditional GitOps | Kapro |
|---|---|---|
| **Runtime source** | Git repository (mutable) | OCI registry (immutable) |
| **Blast radius** | Cluster-wide on bad commit | Controlled via promotion waves |
| **Coordination** | Central Git repo bottleneck | Independent cluster reconciliation |
| **Rollback** | Git revert, wait for sync | Instant artifact tag switch |
| **Auditability** | Git history | Signed OCI provenance chains |

## The Missing Link in Global GitOps

You have a centralized artifact registry. You have thousands of edge clusters running Flux, Helm, and Kustomize. But how do you stage rollouts? Manage cross-cluster canaries? Validate state before promotion?

A fleet of this scale demands a dedicated, state-aware promotion engine.

<p align="center">
  <img src="docs/missing-link.png" alt="The missing link in global GitOps" width="700">
</p>

## Enter Kapro

Kapro doesn't replace the CNCF ecosystem. It choreographs it. A Flux-native, OCI-first promotion engine that introduces **promotion as a first-class Kubernetes primitive** — with automated progressive delivery, health gates, and drift reconciliation built directly into the control plane.

<p align="center">
  <img src="docs/kapro-ecosystem.png" alt="Kapro: the CNCF-native promotion engine" width="700">
</p>

### Three Layers of Promotion

1. **Intra-cluster blue/green** — localized confidence through in-cluster routing and validation before promotion. Rollbacks take milliseconds, not minutes.
2. **Inter-cluster canary rings** — wave-based rollout across cluster rings with automated health gates. Pilot clusters (3 stores) → regional waves (10% → 50% → 100%).
3. **Isolated sovereign workloads** — independent cluster reconciliation with no cross-cluster dependencies at runtime. Each country's clusters reconcile independently.

<p align="center">
  <img src="docs/promotion-mechanics.png" alt="Precision control: the mechanics of promotion" width="700">
</p>

## Autonomous Operations and Bulletproof Reliability

Kapro manages 27-wave dependsOn execution across CRDs, operators, state, apps, and ingress. Automated health gates ensure that if a canary fails, the rollout halts immediately. Zero fleet-wide bad deployments.

<p align="center">
  <img src="docs/autonomous-operations.png" alt="Autonomous operations and bulletproof reliability" width="700">
</p>

## The Byproducts: Security and Efficiency

Because Kapro enforces immutable, operator-driven deployments, static keys are eradicated. Security policies are deployed as code alongside the workloads. Reliable state management enables aggressive disaggregated scaling — non-critical workloads safely scale to zero.

<p align="center">
  <img src="docs/security-efficiency.png" alt="Security and efficiency byproducts" width="700">
</p>

## Use Cases

### Retail: Multi-Country POS Systems
Deploy point-of-sale software to 10,000+ stores across 30+ countries. Pilot clusters first, then regional waves, with country sovereignty — each country's clusters reconcile independently. A bad deployment halts at wave boundaries, never reaches the fleet.

### Financial Services: Regulatory Compliance
Separate deployment flows for GDPR (EU), SOC2 (US), PCI-DSS (global). Environment isolation per regulatory zone, audit trails via signed OCI provenance chains, and mandatory human approval gates for production changes.

### SaaS: Multi-Tenant Platforms
Progressive rollout to customer clusters. Canary tenants get new features first. Health gates block rollout if error rates spike. Automatic promotion to remaining tenants after a configurable soak period.

## How Kapro Compares

| | Kapro | Flux | ArgoCD | Kargo |
|---|---|---|---|---|
| **Multi-cluster promotion** | Native | Manual | App-of-apps | Native |
| **Progressive delivery** | Built-in | Via Flagger | Via Argo Rollouts | Built-in |
| **OCI-first** | Yes | Partial | Git-centric | Yes |
| **Sovereign fleet support** | Designed for it | No | No | Limited |
| **Flux compatibility** | Built on Flux | N/A | No | Separate |
| **Health gates** | Pluggable | No | No | Yes |
| **Manual approvals** | CRD-based | No | External | Yes |

Kapro sits **above** Flux (not replacing it) and **alongside** Kargo (complementary, not competitive). Kapro focuses on **horizontal wave ordering** across sovereign fleets, while Kargo focuses on vertical pipeline staging across environments.

## Getting Started

```bash
# Bootstrap a hub cluster
kapro hub init --project my-project --cluster my-hub

# Add spoke clusters to the fleet
kapro spoke add de-prod --provider gcp-fleet --labels tier=canary
kapro spoke add fi-prod --provider gcp-fleet --labels tier=prod

# Define your app and delivery pipeline
kubectl apply -f kaproapp.yaml
kubectl apply -f kapro.yaml

# Push a version from CI
kapro bundle generate --app my-app --version 1.0.0 --push

# Create a release — Kapro handles the rest
kubectl apply -f release.yaml
```

## Documentation

- [Architecture Spec](docs/SPEC.md)
- [Roadmap](docs/ROADMAP.md)

## Proven at Scale. Open to the Community.

<p align="center">
  <img src="docs/proven-at-scale.png" alt="Proven at scale, open to the community" width="700">
</p>

Kapro is built to be the open-source standard for multi-cluster fleet promotion. Join the project, contribute to the standard, and tame the complexity of global GitOps.

## License

Apache 2.0 — see [LICENSE](LICENSE).
