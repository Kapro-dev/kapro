<p align="center">
  <img src="docs/logo.png" alt="Kapro logo" width="300">
</p>

<h1 align="center">Kapro</h1>

<p align="center"><strong>Kubernetes-native fleet promotion.</strong><br>
Coordinate safe artifact rollout across multi-cluster Kubernetes fleets.</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
  <a href="https://goreportcard.com/report/kapro.io/kapro"><img src="https://goreportcard.com/badge/kapro.io/kapro" alt="Go Report Card"></a>
  <a href="api/v1alpha1"><img src="https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple" alt="API Group"></a>
</p>

---

## What Kapro Is

Kapro is a Kubernetes-native control plane for promoting immutable artifact
versions across a fleet of clusters.

It answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

Kapro owns cross-cluster release ordering, target planning, gate evaluation,
approval state, backend convergence tracking, and auditable status.

It delegates artifact build, manifest rendering, GitOps reconciliation,
in-cluster traffic shaping, and backend-specific rollout strategy to the tools
that already own those jobs.

## The Missing Fleet Layer

You have a centralized OCI artifact registry. You have edge clusters running Flux, Helm, and Kustomize. But between those two ends, three questions remain unanswered:

- How do we stage rollouts?
- How do we manage cross-cluster canaries?
- How do we validate state before promotion?

A fleet of this scale demands a dedicated, state-aware promotion engine.

## The Artifact is the Contract

Kapro decouples CI from deployment. The OCI artifact becomes the single source of runtime truth: immutable tags only. Any git repo, any CI pipeline can produce it. By making the OCI artifact the contract, CI stops being the deployment orchestrator. Runtime git dependency drops to zero.

## Enter Kapro

Kapro does not replace the CNCF ecosystem. It coordinates it.

It sits above GitOps and delivery backends as the fleet promotion layer: deciding
when each target cluster may advance, then asking the configured backend to
apply exactly one version.

## The Mechanics of Promotion

1. **Release planning.** Select target clusters, order waves, and enforce stage concurrency.
2. **Composable gates.** Use soak timers, Prometheus checks, SLO burn rate, signature verification, approvals, CEL, Jobs, webhooks, or plugins.
3. **Backend-neutral apply.** Patch the configured actuator backend and wait for convergence.
4. **Auditable status.** Persist target phase, gate evidence, approvals, events, and release outcome in Kubernetes.

## Conservative Automation and Reliability

Kapro manages wave-based `dependsOn` execution across release pipelines and
target clusters. Cluster state is reconciled through standard controller-runtime
patterns and backend convergence checks.

Automated gates ensure that unclear or failing evidence halts progression before
the next wave. Advanced statistical gate modes are optional; the default path
stays simple and operator-readable.

## Use Cases

### Multi-Region Fleets
Roll out across clusters in multiple countries and regions. Pilot a small group first, expand in waves, and halt automatically if something goes wrong. Each region reconciles independently.

### Regulated Environments
Separate deployment flows per compliance zone. Environment isolation per regulatory boundary, audit trails via signed OCI provenance chains, and mandatory human approval gates before production.

### Edge and Distributed Platforms
Progressive rollout to hundreds or thousands of edge clusters. Canary groups get new versions first. Health gates block rollout if error rates spike. Auto-promotion after a configurable soak period.

## How Kapro Fits

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

Kapro is not a CI engine, traffic manager, generic workflow system, or
replacement for Flux, Argo CD, Argo Rollouts, Flagger, Kargo, or Tekton. See
[Vision and Boundaries](docs/vision-and-boundaries.md) for the project scope.

## Getting Started

```bash
# Bootstrap a hub cluster
kapro hub init --project my-project --cluster my-hub

# Add spoke clusters to the fleet
kapro spoke add de-prod --provider gcp-fleet --labels tier=canary
kapro spoke add fi-prod --provider gcp-fleet --labels tier=prod

# Define your app and delivery pipeline
kubectl apply -f examples/hub-config/apps/checkout.yaml
kubectl apply -f examples/hub-config/pipelines/checkout-progressive.yaml

# Push a version from CI
kapro bundle generate --app my-app --version 1.0.0 --push

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
- [Vision and Boundaries](docs/vision-and-boundaries.md)
- [CNCF Positioning](docs/cncf-positioning.md)
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
