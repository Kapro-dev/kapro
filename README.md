<p align="center">
  <img src="docs/logo.png" alt="Kapro logo" width="300">
</p>

<h1 align="center">Kapro</h1>

<p align="center"><strong>The promotion control plane for Kubernetes fleets.</strong><br>
Kapro coordinates safe artifact promotion across clusters, regions, and clouds while existing GitOps, rollout, traffic, and policy systems execute local changes.</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
  <a href="https://goreportcard.com/report/kapro.io/kapro"><img src="https://goreportcard.com/badge/kapro.io/kapro" alt="Go Report Card"></a>
  <a href="api/v1alpha1"><img src="https://img.shields.io/badge/API-kapro.io%2Fv1alpha1-purple" alt="API Group"></a>
</p>

---

## Project Status

Kapro is **alpha production-capable**, not GA.

The current codebase has working install, promotionrun smoke, Argo brownfield, Flux
brownfield, plugin hot-load, and KPI planner dispatch coverage. It is suitable
for controlled adopters who can run the documented verification and accept
`kapro.io/v1alpha1` API movement.

Do not treat Kapro as GA yet. GA still requires a stable API version, tagged
promotionrun-to-promotionrun upgrade history, broad operator soak, and an independent
security audit. See [GA Readiness](docs/ga-readiness.md) and
[Alpha Production Capability](docs/alpha-production-capability.md) for the
current evidence and exit criteria.

## What Kapro Is

Kapro is a Kubernetes-native control plane for promoting immutable artifact
versions across a fleet of clusters.

It answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

Kapro owns cross-cluster promotionrun ordering, target planning, gate evaluation,
approval state, backend convergence tracking, and auditable status.

It delegates artifact build, manifest rendering, GitOps reconciliation,
in-cluster traffic shaping, and backend-specific rollout strategy to the tools
that already own those jobs.

## The Missing Fleet Layer

You have a centralized OCI artifact registry. You have edge clusters running Flux, Helm, and Kustomize. But between those two ends, three questions remain unanswered:

- How do we stage fleet promotion?
- How do we manage cross-cluster canaries?
- How do we validate state before promotion?

A fleet of this scale demands a dedicated, state-aware promotion control plane.

## The Artifact is the Contract

Kapro decouples CI from deployment. The OCI artifact becomes the single source of runtime truth: immutable tags only. Any git repo, any CI promotionplan can produce it. By making the OCI artifact the contract, CI stops being the deployment orchestrator. Runtime git dependency drops to zero.

## Enter Kapro

Kapro does not replace the CNCF ecosystem. It coordinates it. Kapro is a fleet
deployment promotion control plane: it decides when and where a version may
advance across a fleet; local rollout systems decide how pods, sync, and traffic
changes happen inside each cluster.

It sits above Kubernetes Operators, Helm, Kustomize, OCI registries, GitOps reconciliation loops, Argo CD, Argo Rollouts, Flagger, Istio, Gateway API, and custom plugins as a state-aware promotion control plane.

## The Mechanics of Promotion

1. **Delegated local rollout strategies.** Keep using Kubernetes Deployments, Argo Rollouts, Flagger, Istio, Gateway API, Flux, Argo CD, Helm, or custom actuators for namespace-local rollout and traffic mechanics.
2. **Cross-cluster promotion waves.** Kapro coordinates which targets advance first, which regions wait, and when the global fleet may progress.
3. **Promotion before progression.** Kapro advances only after target health, gates, approvals, plugin status, and policy checks pass; the selected backend executes the local change.
4. **Auditable evidence.** Kapro persists target phase, gate evidence, approvals, lifecycle events, and promotionrun outcome in Kubernetes status.

## Greenfield and Brownfield

Kapro supports both connect paths:

- **Greenfield bootstrap:** create the hub, backend profiles, cluster inventory,
  starter sources, promotionplans, gates, and optional spoke agents from Kapro
  manifests or CLI flows.
- **Brownfield connect:** discover existing Argo CD or Flux topology, observe it
  first, then explicitly adopt selected applications or clusters for promotion.

For Argo CD users, this means Kapro can start from existing cluster Secrets,
Applications, ApplicationSets, and app-of-apps instead of requiring a full
rewrite into Kapro objects on day one. Kapro references backend-owned Secrets
and configuration; it does not copy Argo CD or Flux credentials.

```bash
# greenfield
kapro init ./promotion-repo --backend argo --name checkout

# brownfield
kapro connect argo ./kapro-connect --namespace argocd --selector kapro.io/import=true
```

## Conservative Automation and Reliability

Kapro manages wave-based `dependsOn` execution across promotionrun promotionplans and
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
Progressive promotion to hundreds or thousands of edge clusters. Canary groups get new versions first. Health gates block progression if error rates spike. Auto-promotion after a configurable soak period.

## How Kapro Fits

| | Kapro | Flux | ArgoCD | Kargo |
|---|---|---|---|---|
| **Multi-cluster promotion** | Native | Manual | App-of-apps | Native |
| **Fleet promotion orchestration** | Native | Manual | App-of-apps | Native |
| **OCI-first** | Yes | Partial | Git-centric | Yes |
| **Sovereign fleet support** | Designed for it | No | No | Limited |
| **Backend model** | Backend-neutral control plane | Built-in GitOps engine | Built-in GitOps engine | Separate promotion controller |
| **Health gates** | Pluggable | No | No | Yes |
| **Manual approvals** | CRD-based | No | External | Yes |

Kapro sits **above** local rollout and GitOps systems, not replacing them, and **alongside** Kargo as a complementary tool. Kapro focuses on horizontal wave ordering across sovereign fleets, while local systems handle namespace-level rollout, sync, traffic shifting, and workload health.

Kapro selects delivery systems through `BackendProfile` and
`spec.delivery.backendRef`. Flux and Argo are first-party adapters, and custom
backends can be added through the plugin path.

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

# Define your promotion source and delivery promotionplan
kubectl apply -f examples/hub-config/sources/checkout.yaml
kubectl apply -f examples/hub-config/promotionplans/checkout-progressive.yaml

# Push a version from CI
kapro source package --source checkout --version 1.0.0 --push

# Create a promotionrun. Kapro handles the rest.
kubectl apply -f examples/hub-config/promotionruns/checkout-v1.2.3.yaml
```

Existing users upgrading a hub should read the API stability and upgrade policy
before applying new CRDs or rolling the operator. Plugin users should run the
matching KAI, KGI, or KPI conformance harness before enabling a new plugin image
in production. Large fleets should review the scalability guide before raising
stage parallelism or adding operator replicas.

Quick troubleshooting checks:

- `kubectl get promotionruns,promotiontargets,pluginregistrations` to confirm observed
  generation and readiness caught up.
- Check the operator logs for disabled controllers, shard selection, plugin
  gateway registration, and webhook startup.
- Confirm `KAPRO_HUB_API_URL`, approval secrets, plugin TLS Secrets, and
  notification Secrets are present in the operator namespace.
- For sharded deployments, verify the `kapro.io/shard` label is set when the
  `PromotionRun` is created.

## Documentation

- [Changelog](CHANGELOG.md)
- [Evolution Plan](docs/evolution-plan.md)
- [Install Kapro](docs/install.md)
- [Release Process](docs/release-process.md)
- [Release Notes Guide](docs/release-notes.md)
- [Clean-Clone Install Verification](docs/install-verification.md)
- [Alpha Production Capability](docs/alpha-production-capability.md)
- [GA Readiness](docs/ga-readiness.md)
- [v0.1.0-alpha Release Runbook](docs/release-v0.1.0-alpha.md)
- [Architecture Spec](docs/SPEC.md)
- [Vision and Boundaries](docs/vision-and-boundaries.md)
- [CNCF Positioning](docs/cncf-positioning.md)
- [Local Kind Demo](docs/kind-demo.md)
- [API Stability and Upgrade Policy](docs/api-stability.md)
- [Supported Backend Patterns](docs/supported-backend-patterns.md)
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

Kapro is built to be an open-source standard for multi-cluster fleet promotion. Join the project, contribute to the standard, and help make fleet promotion safer and easier to operate.

## License

Apache 2.0. See [LICENSE](LICENSE).
