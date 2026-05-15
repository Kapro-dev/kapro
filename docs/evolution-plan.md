# Kapro Evolution Plan

This plan turns the current preview feature set into an adoptable project:
easy to install, clear to operate, secure by default, and credible for plugin
authors. The sequence is intentionally milestone-based so work can ship in
small, reviewable increments.

## Current Baseline

Kapro now has the core preview surfaces expected by early platform teams:

- Helm and Kustomize install paths.
- ReleaseTrigger OCI observation with signature verification policy.
- KAI, KGI, and KPI plugin contracts with conformance harnesses.
- Plugin compatibility reporting on `PluginRegistration`.
- RBAC, multi-tenancy, security, API stability, operations, monitoring, and
  Kind demo documentation.
- Example external actuator, gate, and planner plugins.

The next step is not another large feature. It is proving that a new user can
install Kapro, run a complete demo, understand the trust boundaries, and build
a conformant plugin without maintainer help.

## Milestone 0: v0.1.0-alpha Release Candidate

Goal: produce a tagged alpha that reviewers can install from a clean clone and
evaluate in a local cluster.

Acceptance gates:

- `helm lint charts/kapro-operator` passes.
- `helm template kapro charts/kapro-operator --namespace kapro-system --include-crds`
  renders CRDs, controller, RBAC, webhook resources, and baseline config.
- `kubectl kustomize config/default` renders without missing resources.
- `go test ./...` passes.
- `scripts/kind-demo.sh up` completes on a clean machine with Docker, Kind,
  kubectl, Go, and Helm installed.
- `scripts/kind-demo.sh status` shows the demo Release and ReleaseTargets.
- `scripts/kind-demo.sh approve` unblocks production targets.
- `docs/install.md`, `docs/kind-demo.md`, `docs/security.md`,
  `docs/security-model.md`, `docs/plugin-authoring.md`, and
  `docs/conformance.md` are linked from the README.

Release artifacts:

- `CHANGELOG.md` entry for `v0.1.0-alpha`.
- Container image for the operator.
- Helm chart archive.
- Git tag.
- GitHub release notes with install, upgrade, security, and known-limitations
  sections.

## Milestone 1: Adoption Path

Goal: make the first 30 minutes excellent.

Deliverables:

- One-command local demo path documented as the primary reviewer path.
- Clean install troubleshooting table covering CRDs, RBAC, webhook certs,
  plugin readiness, ReleaseTrigger blocking, and approval gates.
- A short "choose your path" README section:
  - local demo;
  - Helm install;
  - plugin author;
  - operator runbook;
  - security review.
- Example screenshots or captured output for release progression, gate waiting,
  approval, and convergence.

Acceptance gates:

- A contributor can run the demo using only the README and `docs/kind-demo.md`.
- The demo exercises `ReleaseTrigger -> Release -> planner -> gates -> actuator`.
- The demo does not require real OCI credentials, external Flux controllers, or
  production signing keys.

## Milestone 2: Security and Trust

Goal: make the trust model explicit enough for CNCF and platform-security
review.

Deliverables:

- Threat model with actors, assets, trust boundaries, and non-goals.
- Plugin trust boundary: registration authority, endpoint trust, TLS, Secret
  ownership, and runtime blast radius.
- OCI/signature trust model for ReleaseTrigger with keyless and key-based
  examples.
- Webhook and gate security guidance.
- Secure-by-default Helm values and documented deviations for local demos.

Acceptance gates:

- A security reviewer can identify who is allowed to create
  `PluginRegistration`, `ReleaseTrigger`, and `Approval` objects.
- Unsigned or untrusted artifacts are documented as blocked when signature
  policy requires verification.
- The docs explain why external plugins do not own Kapro release state.

## Milestone 3: Plugin Ecosystem

Goal: give plugin authors a path from prototype to "Kapro-compatible".

Deliverables:

- Compatibility matrix for Kapro versions and KAI/KGI/KPI contract versions.
- Conformance quickstart for actuator, gate, and planner plugins.
- Example external plugin READMEs that state backend permissions,
  idempotency behavior, timeout behavior, and failure modes.
- "Kapro-compatible plugin" criteria:
  - implements the published proto contract;
  - passes the matching conformance harness;
  - publishes a `PluginRegistration` manifest;
  - documents backend permissions and limits;
  - publishes image digest and tested Kapro version.

Acceptance gates:

- A plugin author can copy one example test and run the matching conformance
  package in their plugin repository.
- Unsupported contract versions produce a visible `Compatible=False` condition.
- The certified-plugin story is clearly future, not a current support promise.

## Milestone 4: Production Operations

Goal: make common operational failures diagnosable before the first production
trial.

Deliverables:

- Runbooks for:
  - release stuck;
  - gate failure rate spike;
  - plugin probe failure;
  - trigger blocked;
  - rollout duration p95 regression;
  - rollback by creating a new Release.
- Prometheus alert rules and Grafana dashboard references in the operations
  guide.
- Scale assumptions and limits for targets per hub, sharding, workqueue
  concurrency, API server QPS, plugin timeouts, and stage `maxParallel`.

Acceptance gates:

- Each alert points to a runbook section.
- Operators can tell whether a stalled rollout is waiting on approval, gate,
  plugin readiness, trigger policy, or actuator convergence.
- Sharding guidance explains object ownership and avoids overlapping shards.

## Milestone 5: API Stabilization

Goal: keep preview velocity while preventing accidental contract churn.

Deliverables:

- API surface table with Alpha, Preview, and Stable classifications.
- Deprecation policy with overlap windows.
- Upgrade policy with CRD-first, plugin-first, operator-rollout ordering.
- Schema compatibility check expectations for future CI.
- `v0.2` checklist for promotion candidates that can move from Alpha to
  Preview or Preview to Stable.

Acceptance gates:

- Any breaking change to a Preview surface includes release notes and migration
  guidance.
- Proto field removals reserve field numbers.
- CRD semantic changes use additive fields, conversion, or a new API version.

## Milestone 6: Policy-Bound Agentic Workflows

Goal: let agents assist fleet promotion without making agents part of the core
rollout authority.

Positioning:

```text
Kapro enables policy-bound agentic workflows for fleet promotion.
```

Agents consume release state, planner status, gate evidence, lifecycle events,
and audit history. They may explain, recommend, draft context, request approval,
or create bounded recommendation objects. They must not bypass gates, approvals,
RBAC, admission policy, or the deterministic release state machine.

Deliverables:

- `AgentRecommendation` API preview for explain/recommend/hold/rollback/request
  approval outputs.
- Evidence references from recommendations to `ReleaseTarget.status.gates[]`.
- `AgentPolicy` enforcement for allowed intents, stages, targets, and actions.
- AgentGateway design doc describing context payload, response schema, timeout,
  retry, authn/authz, and audit behavior.
- Admission and RBAC guidance for agent identities.
- Conformance scenarios for safe agent behavior.

Acceptance gates:

- Agents can explain a stuck Release using only Kubernetes status and evidence.
- Agents can propose actions but cannot directly call actuators or mutate
  delivery backends.
- Production-impacting actions require explicit policy and, where configured,
  human approval.
- Kapro rollout execution remains deterministic when no agent is installed.

## Recommended Immediate Work Order

1. Run and repair the Kind demo from a clean clone.
2. Cut `v0.1.0-alpha` release notes and tag after install/demo verification.
3. Polish the security review path and ReleaseTrigger signature examples.
4. Publish the plugin compatibility and conformance quickstart as the external
   plugin author entry point.
5. Add runbook links to each shipped Prometheus alert.
6. Add API compatibility checks to CI before broadening the preview API.
7. Add `AgentRecommendation` API preview and AgentGateway design after the
   alpha install/demo path is stable.

## Done Means

The evolution plan is complete when an outside reviewer can:

1. Install Kapro into Kind.
2. Run the local demo end to end.
3. See where security boundaries are enforced.
4. Build and test a conformant plugin.
5. Diagnose a stuck release from metrics, events, and status.
6. Understand which APIs are alpha, preview, or stable before adopting them.
