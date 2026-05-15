<p align="center">
  <img src="docs/logo.png" alt="Kapro logo" width="300">
</p>

<h1 align="center">Kapro</h1>

<p align="center"><strong>The promotion control plane for Kubernetes fleets.</strong><br>
Kapro coordinates safe version promotion across clusters, regions, and clouds while existing GitOps, rollout, traffic, and policy systems execute local changes.</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
  <a href="https://goreportcard.com/report/kapro.io/kapro"><img src="https://goreportcard.com/badge/kapro.io/kapro" alt="Go Report Card"></a>
  <a href="api/v1alpha1"><img src="https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple" alt="API Group"></a>
</p>

---

## The Fleet-Scale Imperative

Sovereign hubs. Edge locations at true distributed scale. Zero tolerance for centralized runtime coordination, because centralized orchestration is a single point of failure.

When deploying at this magnitude, standard CI/CD pipelines collapse under the weight of drift, state, and sheer volume.

## Why Sequential Pipelines Break

Traditional CI/CD assumes a linear world: build, test, deploy. But modern platforms are different.

Kafka must run before 14 dependent services. Operators must precede custom resources. Databases cannot be simply redeployed. They require lifecycle management, backups, and drift correction. And every cluster must continuously self-verify against its source of truth. Auto-correction is not optional, it is mandatory.

Sequential pipelines simply cannot express this.

## The Missing Link in Global GitOps

You have a centralized OCI artifact registry. You have edge clusters running Flux, Helm, and Kustomize. But between those two ends, three questions remain unanswered:

- How do we stage fleet promotion?
- How do we manage cross-cluster canaries?
- How do we validate state before promotion?

A fleet of this scale demands a dedicated, state-aware promotion control plane.

## The Artifact is the Contract

Kapro decouples CI from deployment. The OCI artifact becomes the single source of runtime truth: immutable tags only. Any git repo, any CI pipeline can produce it. By making the OCI artifact the contract, CI stops being the deployment orchestrator. Runtime git dependency drops to zero.

## Enter Kapro

Kapro does not replace the CNCF ecosystem. It coordinates it. Kapro decides when and where a version may advance across a fleet; local rollout systems decide how pods, sync, and traffic changes happen inside each cluster.

It sits above Kubernetes Operators, Helm, Kustomize, OCI registries, GitOps reconciliation loops, Argo CD, Argo Rollouts, Flagger, Istio, Gateway API, and custom plugins as a single, state-aware promotion control plane.

## The Mechanics of Promotion

1. **Delegated local rollout strategies.** Keep using Kubernetes Deployments, Argo Rollouts, Flagger, Istio, Gateway API, Flux, Argo CD, Helm, or custom actuators for namespace-local rollout and traffic mechanics.
2. **Cross-cluster promotion waves.** Kapro coordinates which targets advance first, which regions wait, and when the global fleet may progress.
3. **Promotion before progression.** Kapro advances only after target health, gates, approvals, plugin status, and policy checks pass; the selected backend executes the local change.

## Autonomous Operations and Bulletproof Reliability

Kapro manages wave-based dependsOn execution across CRDs, operators, state, apps, and ingress. Cluster state is continuously mapped to the Git/OCI source of truth using standard controller-runtime patterns.

Automated health gates ensure that if a target check, local rollout controller, telemetry signal, or approval fails, fleet progression halts before the next targets advance.

## Use Cases

### Multi-Region Fleets
Roll out across clusters in multiple countries and regions. Pilot a small group first, expand in waves, and halt automatically if something goes wrong. Each region reconciles independently.

### Regulated Environments
Separate deployment flows per compliance zone. Environment isolation per regulatory boundary, audit trails via signed OCI provenance chains, and mandatory human approval gates before production.

### Edge and Distributed Platforms
Progressive promotion to hundreds or thousands of edge clusters. Canary groups get new versions first. Health gates block progression if error rates spike. Auto-promotion after a configurable soak period.

## How Kapro Compares

| | Kapro | Flux | ArgoCD | Kargo |
|---|---|---|---|---|
| **Multi-cluster promotion** | Native | Manual | App-of-apps | Native |
| **Fleet promotion orchestration** | Native | Manual | App-of-apps | Native |
| **OCI-first** | Yes | Partial | Git-centric | Yes |
| **Sovereign fleet support** | Designed for it | No | No | Limited |
| **Flux compatibility** | Built on Flux | N/A | No | Separate |
| **Health gates** | Pluggable | No | No | Yes |
| **Manual approvals** | CRD-based | No | External | Yes |

Kapro sits **above** local rollout and GitOps systems, not replacing them, and **alongside** Kargo as a complementary tool. Kapro focuses on horizontal wave ordering across sovereign fleets, while local systems handle namespace-level rollout, sync, traffic shifting, and workload health.

## Getting Started

```bash
# Bootstrap a hub cluster
kapro hub init --project my-project --cluster my-hub

# Add spoke clusters to the fleet
kapro spoke add de-prod --provider gcp-fleet --labels tier=canary
kapro spoke add fi-prod --provider gcp-fleet --labels tier=prod

# Define your bundle and delivery pipeline
kubectl apply -f examples/hub-config/bundles/checkout.yaml
kubectl apply -f examples/hub-config/pipelines/checkout-progressive.yaml

# Push a version from CI
kapro bundle generate --bundle my-bundle --version 1.0.0 --push

# Create a release. Kapro handles the rest.
kubectl apply -f examples/hub-config/releases/checkout-v1.2.3.yaml
```

Existing users upgrading a hub should read the API stability and upgrade policy
before applying new CRDs or rolling the operator. Plugin users should run the
matching KAI, KGI, or KPI conformance harness before enabling a new plugin image
in production. Large fleets should review the scalability guide before raising
stage parallelism or adding operator replicas.

Quick troubleshooting checks:

- `kubectl get releases,releasetargets,pluginregistrations` to confirm observed
  generation and readiness caught up.
- Check the operator logs for disabled controllers, shard selection, plugin
  gateway registration, and webhook startup.
- Confirm `KAPRO_HUB_API_URL`, approval secrets, plugin TLS Secrets, and
  notification Secrets are present in the operator namespace.
- For sharded deployments, verify the `kapro.io/shard` label is set when the
  `Release` is created.

## Documentation

- [Changelog](CHANGELOG.md)
- [Evolution Plan](docs/evolution-plan.md)
- [Install Kapro](docs/install.md)
- [Release Process](docs/release-process.md)
- [Release Notes Guide](docs/release-notes.md)
- [Clean-Clone Install Verification](docs/install-verification.md)
- [v0.1.0-alpha Release Runbook](docs/release-v0.1.0-alpha.md)
- [Architecture Spec](docs/SPEC.md)
- [Local Kind Demo](docs/kind-demo.md)
- [API Stability and Upgrade Policy](docs/api-stability.md)
- [Conformance Packages](docs/conformance.md)
- [Plugin Compatibility](docs/plugin-compatibility.md)
- [Controller Scalability and Resilience](docs/controller-scalability.md)
- [Security Policy](SECURITY.md)
- [Security Implementation Guide](docs/security.md)
- [RBAC, Multi-Tenancy, and Security Model](docs/security-model.md)
- [Plugin Authoring](docs/plugin-authoring.md)
- [Extension Model](docs/extension-model.md)
- [Roadmap](docs/ROADMAP.md)
- [RBAC and Tenancy Model](docs/rbac-tenancy.md)
- [Operations](docs/operations.md)

## Contributing

Kapro is built to be the open-source standard for multi-cluster fleet promotion. Join the project, contribute to the standard, and tame the complexity of global GitOps.

## License

Apache 2.0. See [LICENSE](LICENSE).
