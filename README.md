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

## The Fleet-Scale Imperative

Sovereign hubs. Edge locations at true distributed scale. Zero tolerance for centralized runtime coordination, because centralized orchestration is a single point of failure.

When deploying at this magnitude, standard CI/CD pipelines collapse under the weight of drift, state, and sheer volume.

<p align="center">
  <img src="docs/fleet-scale.png" alt="The fleet-scale imperative" width="700">
</p>

## Why Sequential Pipelines Break

Traditional CI/CD assumes a linear world: build, test, deploy. But modern platforms are different.

Kafka must run before 14 dependent services. Operators must precede custom resources. Databases cannot be simply redeployed. They require lifecycle management, backups, and drift correction. And every cluster must continuously self-verify against its source of truth. Auto-correction is not optional, it is mandatory.

Sequential pipelines simply cannot express this.

<p align="center">
  <img src="docs/sequential-pipelines-break.png" alt="Why sequential pipelines break" width="700">
</p>

## The Missing Link in Global GitOps

You have a centralized OCI artifact registry. You have edge clusters running Flux, Helm, and Kustomize. But between those two ends, three questions remain unanswered:

- How do we stage rollouts?
- How do we manage cross-cluster canaries?
- How do we validate state before promotion?

A fleet of this scale demands a dedicated, state-aware promotion engine.

<p align="center">
  <img src="docs/missing-link.png" alt="The missing link in global GitOps" width="700">
</p>

## The Artifact is the Contract

Kapro decouples CI from deployment. The OCI artifact becomes the single source of runtime truth: immutable tags only. Any git repo, any CI pipeline can produce it. By making the OCI artifact the contract, CI stops being the deployment orchestrator. Runtime git dependency drops to zero.

<p align="center">
  <img src="docs/artifact-contract.png" alt="Multi-pipeline GitOps: the artifact is the contract" width="700">
</p>

## Enter Kapro

Kapro doesn't replace the CNCF ecosystem. It choreographs it. An open-source orchestrator built to manage the complex state of modern sovereign fleets.

It sits at the center of Kubernetes Operators, Helm, Kustomize, OCI registries, and GitOps reconciliation loops, coordinating them into a single, state-aware promotion engine.

<p align="center">
  <img src="docs/kapro-ecosystem.png" alt="Kapro: the CNCF-native promotion engine" width="700">
</p>

## The Mechanics of Promotion

<p align="center">
  <img src="docs/promotion-mechanics.png" alt="Precision control: the mechanics of promotion" width="700">
</p>

1. **Intra-cluster blue/green.** Seamless traffic cutover within cluster boundary. Rollbacks are instant routing flips, not redeployments.
2. **Inter-cluster canary.** Progressive rollout with health gates across regions. Pilot clusters first, then regional waves, then the global fleet.
3. **No inline updates. Ever.** Traffic is cutover only after verification. Standby workloads always deploy in a separate namespace. No more mutating live state.

## Autonomous Operations and Bulletproof Reliability

Kapro manages wave-based dependsOn execution across CRDs, operators, state, apps, and ingress. Cluster state is continuously mapped to the Git/OCI source of truth using standard controller-runtime patterns.

Automated health gates ensure that if a check or canary fails, the rollout halts. Zero fleet-wide bad deployments.

<p align="center">
  <img src="docs/autonomous-operations.png" alt="Autonomous operations and bulletproof reliability" width="700">
</p>

## Use Cases

### Retail: Multi-Country POS Systems
Deploy point-of-sale software to thousands of stores across 30+ countries. Pilot clusters first, then regional waves. Each country's clusters reconcile independently. A bad deployment halts at wave boundaries and never reaches the fleet.

### Financial Services: Regulatory Compliance
Separate deployment flows for GDPR, SOC2, and PCI-DSS. Environment isolation per regulatory zone, audit trails via signed OCI provenance chains, and mandatory human approval gates for production changes.

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

Kapro sits **above** Flux, not replacing it, and **alongside** Kargo as a complementary tool. Kapro focuses on horizontal wave ordering across sovereign fleets, while Kargo focuses on vertical pipeline staging across environments.

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

# Create a release. Kapro handles the rest.
kubectl apply -f release.yaml
```

## Documentation

- [Architecture Spec](docs/SPEC.md)
- [Roadmap](docs/ROADMAP.md)

## Contributing

Kapro is built to be the open-source standard for multi-cluster fleet promotion. Join the project, contribute to the standard, and tame the complexity of global GitOps.

## License

Apache 2.0. See [LICENSE](LICENSE).
