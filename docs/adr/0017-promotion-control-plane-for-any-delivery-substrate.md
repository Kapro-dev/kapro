# ADR-0017: Promotion Control Plane For Any Delivery Substrate

## Status
Accepted

## Context

Kapro's product vision is a promotion control plane: one place to decide
whether a version may advance, record why that decision was made, and hand the
delivery action to whatever system the platform already uses.

Real adopters do not standardize on one delivery engine. A single organization
may run Argo CD, Flux, direct Kubernetes apply, OCI bundle delivery, Tekton,
GitLab pipelines, internal deployment APIs, and migration paths from systems
such as Kargo. Some flows are Git-backed, some are Gitless, some are
artifact-backed, and some are API-driven.

ADR-0016 introduced `SubstrateClass`, typed config CRDs, and KSI as the
hub-side technical contract for this extension model. Spoke-side pull delivery
uses KSP (`pkg/spokeprovider.Provider`) when work must execute from the
cluster-side controller. This ADR records the higher-level positioning decision
behind those contracts: Kapro should be delivery-engine neutral while still
retaining ownership of promotion policy, workflow, approval, trace, and
compliance semantics.

## Decision

Kapro is a promotion control plane for any delivery substrate that can expose
deterministic validate, apply, observe, status, capability, and evidence
semantics through the substrate contract.

This includes, but is not limited to:

- GitOps engines such as Argo CD and Flux;
- Git-backed CI/CD or environment systems such as GitLab when represented by a
  conformant substrate package;
- Gitless engines such as direct Kubernetes apply, Helm chart delivery, OCI
  bundle delivery, model-serving APIs, or internal deployment APIs;
- pipeline systems such as Tekton, Jenkins, GitHub Actions, or GitLab CI when
  the substrate can start work, observe progress, and report deterministic
  terminal or retryable status;
- fleet add-on or platform delivery systems such as Sveltos when represented as
  a conformant optional substrate;
- platform APIs and custom APIs through webhook or domain-specific substrate
  packages;
- coexistence or migration adapters for peer promotion systems such as Kargo,
  provided Kapro remains the promotion authority for the Kapro-managed flow.

The durable API taxonomy remains technical rather than marketing-shaped:

```yaml
metadata:
  labels:
    kapro.io/family: gitops | direct | artifact | platform | pipeline | custom
```

Marketing and docs may use words such as Gitful, Gitless, BYOD, Argo-native,
or custom API delivery. CRDs and conformance labels use the stable technical
families above.

Kapro core owns:

- policy evaluation;
- promotion gates and guardrails;
- approvals;
- workflow state;
- decision traces and standard events;
- compliance evidence assembly.

Substrate implementations own:

- substrate-native validation;
- apply, observe, rollback, staging, and discovery primitives;
- capability reporting;
- substrate-native status and object evidence;
- translation from Kapro desired versions to native fields, artifacts,
  manifests, pipeline variables, or API requests.

## Pipeline substrate candidates

Pipeline systems are valid Kapro substrates when they can be triggered with a
Kapro correlation identity and observed until a deterministic terminal or
retryable state is reached.

| Candidate | Family | Substrate mapping |
| --- | --- | --- |
| Tekton Pipelines | `pipeline` | Create a `PipelineRun`, pass params, observe `PipelineRun.status.conditions`, child TaskRuns, results, and events. |
| Argo Workflows | `pipeline` | Submit a Workflow or WorkflowTemplate, observe workflow phase, nodes, outputs, and archived logs. |
| GitLab CI/CD | `pipeline` | Trigger or create a pipeline with variables or inputs, store the returned pipeline ID, observe pipeline and job status. |
| GitHub Actions | `pipeline` | Dispatch a workflow or repository event, correlate the resulting run, observe workflow run, job, log, and artifact state. |
| Custom API / webhook | `pipeline`, `platform`, or `custom` | Dispatch a platform-defined workflow and observe deterministic status through the API contract. |

Other systems such as Azure DevOps Pipelines, Buildkite, CircleCI, Jenkins,
TeamCity, Concourse, AWS CodePipeline, Google Cloud Build, and Harness are
valid long-term candidates, but they are not first public-preview commitments.

## Platform substrate candidates

Platform delivery systems are valid Kapro substrates when Kapro remains the
promotion authority and the platform exposes deterministic delivery and status
evidence.

| Candidate | Family | Substrate mapping |
| --- | --- | --- |
| Helm direct delivery | `direct` | Kapro renders or upgrades a Helm chart using a typed release config and observes release/workload status. Chart input may come from a Helm repository, local bundle, Git, or OCI registry. |
| Sveltos | `platform` or `direct` | Kapro decides promotion, then creates or updates Sveltos `ClusterProfile` or `Profile` intent and observes add-on/app deployment state. |
| Internal deployment platform | `platform` or `custom` | Kapro dispatches through a typed platform config or custom API and records platform-native rollout evidence. |
| Kargo coexistence adapter | `platform` or `custom` | Kapro imports, observes, or hands off migration/coexistence flows while avoiding two promotion authorities for the same managed flow. |

The early implementation order should prefer systems with the cleanest
contract:

1. Tekton, because it is Kubernetes-native and maps directly to typed CRDs and
   controller reconciliation.
2. GitLab CI/CD, because it proves a Git-backed external pipeline substrate.
3. GitHub Actions, because it is common and API-triggerable.
4. Custom API / webhook, because it proves platform-owned workflow integration
   without forcing Kapro to own every third-party pipeline implementation.

## Implementation TODOs

- Add a `pipeline` substrate conformance profile covering correlation IDs,
  trigger idempotency, observe semantics, terminal status mapping, retryable
  failure mapping, cancellation, and evidence fields.
- Define the standard Kapro correlation fields passed to pipeline substrates:
  PromotionRun UID, target, desired version, request ID, source revision, and
  decision trace ID.
- Add a generic pipeline evidence shape for URLs, run IDs, job IDs, commit or
  artifact revisions, started/completed timestamps, status, logs, and artifact
  references.
- Build the first reference `tekton` substrate with typed config and binding
  CRDs.
- Build a `gitlab-pipeline` substrate package after Tekton proves the profile.
- Treat GitHub Actions and custom API/webhook as the next named examples.
- Leave Azure DevOps, Buildkite, CircleCI, Jenkins, TeamCity, Concourse, AWS
  CodePipeline, Google Cloud Build, Harness, and similar systems to third-party
  `SubstrateClass` implementations until the conformance suite is stable.
- Keep direct Kubernetes apply as the smallest default delivery path. Treat OCI
  as one artifact source, not a mandatory default dependency.
- Keep KSI and KSP distinct in documentation and conformance: KSI defines the
  public hub-side substrate package/class contract, while KSP is only required
  for substrate implementations that need spoke-side pull execution.
- Add Helm direct delivery as either a renderer behind `kubernetes-apply` or a
  dedicated `helm-upgrade` substrate once release-state ownership is specified.
- Add a `sveltos` substrate only after the default direct delivery path is
  stable; do not make Sveltos a hard Kapro dependency.
- Keep `webhook` as the escape hatch for simple custom API integrations, but
  require domain-specific substrate packages when richer status, evidence,
  cancellation, rollback, or discovery is needed.

## Rejected alternatives

### Make Kapro a GitOps-only promotion layer

This would simplify the first implementation but erase the product
differentiator. Many adopters want GitOps support for Argo CD and Flux, but
they also need direct Kubernetes apply, OCI, platform APIs, or pipeline-backed
delivery. Kapro should support GitOps without making Git the only ledger or
delivery path.

### Make Kargo the foundational Argo delivery substrate

Kargo is itself a promotion orchestration layer with its own stage and freight
model. Treating it as the default Kapro substrate would stack two promotion
control planes and blur ownership of policy, approvals, and workflow state.

Kapro may add Kargo coexistence, import, or migration adapters later. The first
Argo path remains direct Argo CD substrate support.

### Put policy and guardrail conformance inside every substrate

That would force every substrate author to import or reimplement Kapro's policy
language and workflow semantics. It would also make custom API, pipeline, and
third-party integrations harder to write safely.

Kapro core evaluates policy before dispatch. Substrates report what they can do
and what happened when they did it.

### Use marketing labels as CRD taxonomy

Labels such as `argo-native`, `gitless-api`, or `byod` are useful public
language but poor API vocabulary. They are vendor-bound or colloquial and will
age as the ecosystem changes. The API taxonomy should describe execution
families, not campaigns.

### Promise support for literally arbitrary side effects

"Any delivery substrate" does not mean Kapro can safely drive any side effect
with no contract. A conformant substrate must provide deterministic validation,
idempotent apply behavior, observable status, capability reporting, and enough
evidence for traces and audits. Systems that cannot provide those semantics can
still be invoked externally, but they are not conformant Kapro substrates.

## Consequences

- The image-level vision is valid: Kapro can sit above Gitful, Gitless, BYOD,
  pipeline, custom API, and platform delivery paths.
- ADR-0016 remains the implementation foundation. This ADR does not replace the
  class/config contract; it explains why that contract exists.
- Reference substrates should continue to prove multiple families over time,
  but the `0.6.2` launch set is intentionally narrow: `kubernetes-apply`,
  `argo`, `flux`, and `oci`. Webhook/custom API delivery remains a valid
  substrate family after a concrete actuator, status model, and conformance
  profile exist; 0.6 should not ship empty webhook delivery CRDs.
- OCI is intentionally included as the fourth `0.6.2` reference substrate
  because it proves artifact-backed Gitless delivery and spoke-side execution.
  It is not a default dependency for direct Kubernetes apply, Helm rendering,
  or raw manifest delivery.
- The `0.6.2` launch set must pass an internal Go substrate conformance suite.
  A public `kapro substrate conformance <class>` CLI can follow in `0.7.x`
  after the reference contract has proved itself.
- The `0.6.2` launch set is a transition state: KSI reference scenarios prove
  the public substrate contract, KSP provider conformance proves spoke-side
  provider behavior where needed, and the current in-tree direct, Argo CD, Flux,
  and OCI runtime paths are still covered by actuator/controller tests until
  those adapters expose native KSI implementations or tested KSI bridges.
- `tekton`, GitLab-style pipeline delivery, and similar systems fit the
  `pipeline` family once they can meet KSI and conformance requirements.
- Kargo belongs in a later coexistence or migration lane unless a specific
  adapter can preserve Kapro's authority over the Kapro-managed promotion flow.
- Sveltos can be an optional substrate for fleet add-on and application
  delivery, but Kapro's default delivery path remains Kapro-native direct apply.
- Custom APIs remain first-class in the model, but 0.6 implements them through
  domain-specific substrate packages first. A generic webhook substrate should
  ship only after it can report deterministic status, evidence, cancellation,
  rollback, and conformance results.

## References

- ADR-0016: Substrate class and typed config contract
- `docs/specs/substrate-parameter-spec.md`
- `docs/concepts/kargo-comparison.md`
- Tekton Pipeline API: https://tekton.dev/docs/pipelines/pipeline-api/
- Argo Workflows REST API: https://argo-workflows.readthedocs.io/en/latest/rest-api/
- GitLab pipeline triggers: https://docs.gitlab.com/ci/triggers/
- GitLab Pipelines API: https://docs.gitlab.com/api/pipelines/
- GitHub Actions REST API: https://docs.github.com/en/rest/reference/actions
- Azure DevOps Pipelines Runs API: https://learn.microsoft.com/en-us/rest/api/azure/devops/pipelines/runs
- Buildkite Builds API: https://buildkite.com/docs/apis/rest-api/builds
- CircleCI pipeline triggers: https://circleci.com/docs/triggers-overview/
- Jenkins Remote Access API: https://wiki.jenkins.io/JENKINS/Remote-access-API.html
- TeamCity build queue API: https://www.jetbrains.com/help/teamcity/rest/buildqueueapi.html
- Concourse jobs: https://concourse-ci.org/docs/jobs/
- AWS CodePipeline StartPipelineExecution: https://docs.aws.amazon.com/codepipeline/latest/APIReference/API_StartPipelineExecution.html
- Google Cloud Build create build API: https://docs.cloud.google.com/build/docs/api/reference/rest/v1/projects.builds/create
- Harness custom triggers: https://developer.harness.io/docs/platform/triggers/trigger-deployments-using-custom-triggers/
- Helm upgrade: https://helm.sh/docs/helm/helm_upgrade/
- Sveltos add-on distribution: https://projectsveltos.io/v1.3.0/addons/addons/
