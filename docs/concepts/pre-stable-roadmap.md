# Pre-stable Roadmap

Kapro's roadmap stays in the `0.x.x` series until the public CRDs, Go SDK,
plugin contracts, conformance tests, upgrade behavior, and operational defaults
have proved stable across real release trains. The first version digit remains
`0` for roadmap work.

This page is a planning guide, not a compatibility promise. The binding record
for a release is still `CHANGELOG.md` plus the release notes for that tag.

GitHub milestones must use exact pre-stable semver names. Use names such as
`v0.2.4`, `v0.4.7`, or `v0.4.20`; do not use shorthand names such as `v0.6`
or broad train-start buckets such as `v0.10.0`.

The numbering strategy is `0.<capability-line>.<feature-increment>`. The second
digit groups a capability line; the third digit names the concrete feature
increment inside that line.

## Roadmap Lines

| Line | Theme | Practical ship criteria |
| --- | --- | --- |
| `0.2.x` | Programmable engine hardening | AdapterPolicy discovery is real, programmable gates are documented and tested, release-train policy is enforced, and retention metrics are available before opt-in GC. |
| `0.4.x` | Adoption and operator ergonomics | `pkg/kapro/server` can be assembled from smaller registrars, CLI adoption paths are observe-first by default, and existing GitOps adopters have clear rollback points. |
| `0.6.x` | Ecosystem and conformance | External adapter authors can run conformance locally, at least one substrate adapter proves the plugin contract outside the in-tree controller path, and examples compile in CI. |
| `0.8.x` | Operational scale and security | Upgrade, rollback, observability, tenancy, signing, provenance, and failure-mode tests are strong enough for production change-control review. |

Concrete milestones inside those lines still need all three digits, for example
`v0.4.7` or `v0.4.20`. Do not create a milestone until the feature increment is
specific enough to name that patch digit.

Patch increments are a planning budget, not a promise that every capability
line stops at `.10`. Once a line crosses roughly 10 increments, do an explicit
scope review: either continue the line with concrete milestones such as
`v0.4.20` or `v0.5.8`, or move the next work into a new capability line. Avoid
placeholder milestones such as `v0.10.0`, `v0.20.0`, or `v0.30.0` unless that
exact patch release has a real feature scope.

## Train Rules

- Keep user-facing work in narrow PRs that can be reviewed and merged
  independently.
- Prefer finishing a shipped preview surface over adding a new public CRD or
  SDK type.
- Do not widen public schemas without an immediate runtime path, documentation,
  and a migration story.
- Add or update tests in the same PR as behavior changes.
- Treat docs, examples, and conformance as part of the product, not as release
  cleanup.

## Step-by-step Plan To `0.6.x`

This sequence turns ADR-0017 into shippable work. It is a planning order, not a
promise that every item maps one-to-one to a GitHub milestone. When work is
promoted to a milestone, use exact patch versions such as `v0.5.11` or
`v0.6.1`.

1. **Lock the public-preview scope.** `0.6.0` is GitOps Bootstrap Preview:
   direct delivery, Argo CD, Flux, greenfield generation, and existing GitOps
   connect/adopt flows. ADR-0017's "any substrate" vision remains long-term
   positioning, not the first public release promise.
2. **Lock the default boundary.** Keep Kapro's smallest default
   delivery path Kapro-native through direct Kubernetes apply. Treat OCI as an
   artifact source, and treat Helm, Argo CD, Flux, webhook, Sveltos, Tekton,
   GitLab, and other systems as optional substrates or renderers, not hard
   dependencies.
3. **Stabilize the class/config contract.** Finish docs, examples,
   controller status, RBAC, migration guidance, and conformance checks around
   `SubstrateClass`, `Backend.spec.classRef`, and `Backend.spec.configRef`.
   `docs/specs/substrate-parameter-spec.md` must ship publicly in `0.6.0` as
   the `v1alpha2` author contract, with breaking changes still allowed while
   the surface remains alpha.
4. **Prove launch substrate families.** Keep the `0.6.0` reference set focused
   on `kubernetes-apply`, `argo-cd`, and `flux`. OCI and webhook stay valid
   substrate families, but they must not distract the first public preview from
   direct apply plus the two CNCF GitOps engines.
5. **Clarify rendering versus delivery.** `Source` remains the unit/source
   catalog. Rendering turns raw YAML, Helm, or Kustomize into Kubernetes
   objects. Delivery applies or delegates those objects through a substrate.
   For the direct profile, render before `kubernetes-apply`; for Argo CD and
   Flux, let the GitOps engine perform its native rendering/reconciliation.
6. **Clarify Helm direct delivery.** Implement Helm first as render-to-manifest
   behind direct apply. Treat a dedicated `helm-upgrade` substrate as later work
   because it gives Helm release-state ownership, hooks, and rollback semantics.
7. **Build `bootstrapgen` with a small matrix.** Replace hardcoded scaffold
   strings with schema-backed embedded templates, but ship only three canonical
   profile/template pairs first: `direct` + raw YAML, `argocd` + Helm, and
   `flux` + Kustomize.
8. **Define the pipeline substrate profile.** Specify correlation IDs,
   idempotent triggering, observe semantics, terminal and retryable status
   mapping, cancellation, and evidence before implementing individual pipeline
   packages.
9. **Implement `tekton` as the first pipeline substrate.** Use Tekton to prove
   the `pipeline` family with Kubernetes-native CRDs and controller
   reconciliation after `0.6.0`.
10. **Implement `gitlab-pipeline` as the first external pipeline substrate.**
   Use GitLab CI/CD to prove Git-backed external pipeline delivery with API
   triggering, run observation, and evidence capture after Tekton.
11. **Decide the optional platform lane.** Model `sveltos` as an optional
   `platform` or `direct` substrate for fleet add-on/application delivery after
   the default Kapro-native path is stable. Do not copy Sveltos into Kapro
   core.
12. **Open the third-party substrate path.** In `0.6.0`, ship the internal Go
   conformance suite for `kubernetes-apply`, `argo-cd`, and `flux`. Promote it
   to a public `kapro substrate conformance <class>` CLI in `0.7.x` after the
   launch substrates prove the contract.

## Technical Foundation

All delivery work in this roadmap must connect back to ADR-0016's substrate
contract:

- `SubstrateClass` declares the delivery implementation and capability family.
- `Backend.spec.classRef` and `Backend.spec.configRef` select the substrate and
  its typed configuration.
- KSI is the Go contract for `Validate`, `Apply`, `Observe`, and
  `Capabilities`.
- `docs/specs/substrate-parameter-spec.md` is the public author contract.
- `Source` describes the units Kapro promotes; it is not itself the delivery
  executor.

`bootstrapgen` is a generator. It writes repos and manifests. It does not own
runtime delivery.

## GitOps Bootstrap Preview Scope

The first public preview should ship a focused matrix:

| Profile | Canonical app template | Generated Kapro objects |
| --- | --- | --- |
| `direct` | raw YAML | `SubstrateClass`, `Backend`, `Cluster`, `Fleet`, `Plan`, `Promotion` |
| `argocd` | Helm | `SubstrateClass`, `Backend`, `Cluster`, `Fleet`, `Plan`, `Promotion` |
| `flux` | Kustomize | `SubstrateClass`, `Backend`, `Cluster`, `Fleet`, `Plan`, `Promotion` |

Additional app templates are allowed after the canonical three pass smoke tests.
Do not create the full profile x app-template cross-product before `0.6.0`.

The substrate conformance suite is part of `0.6.0`, even if the public CLI
wrapper is not. The three launch substrates must pass the same Go contract so
they work by design, not by fixture coincidence, and so third-party authors have
a reference to copy before `0.7.x` opens broader substrate packaging.

Each generated repo should include minimal CI that runs YAML validation,
`kapro plan` or the nearest available static planner, and optional policy tests
when a policy pack is enabled.

Existing GitOps adoption output must use the same repo shape where possible and
remain observe-first by default. The public CLI should use `connect`,
`discover`, and `adopt`; `brownfield` is internal migration vocabulary and
should not be the marketed command name.

Generated repos are frozen at generation time in milestone 1. Upgrade tooling
such as `kapro bootstrap diff` or `kapro bootstrap upgrade` is Phase 2.

## Public Preview Success Criteria

`0.6.0` is ready only when:

- `direct`, `argocd`, and `flux` profiles each have an end-to-end demo that
  runs `init -> generate -> plan -> promote dev -> stage -> prod` on kind.
- `kubernetes-apply`, `argo-cd`, and `flux` reference substrates pass the
  internal Go substrate conformance suite.
- `docs/specs/substrate-parameter-spec.md` is published as the `v1alpha2`
  substrate author contract.
- `kapro adopt argo` and `kapro adopt flux` work against real Argo CD and Flux
  installs, not only unit tests or repository fixtures.
- Greenfield and existing GitOps connect/adopt walkthroughs exist for Argo CD
  and Flux.
- One quickstart exists for each public profile: `direct`, `argocd`, and
  `flux`.
- Direct delivery has a five-minute quickstart with raw YAML and a documented
  Helm render path.
- Generated repos include reviewable CI and do not require an OCI registry.
- Decision evidence is queryable through existing `DecisionTrace` objects and
  documented CLI/status flows.
- At least 10 internal dogfood issues from generated-repo and adoption runs are
  closed before the public preview tag.
- At least one internal dogfood repo has completed repeated greenfield and
  existing GitOps adoption runs without manual manifest surgery.

## Permanent Product Non-goals

These are identity boundaries, not deferred backlog items:

- Kapro is not a Helm registry.
- Kapro is not a CI runner.
- Kapro is not a manifest store.
- Kapro is not a cluster provisioner.
- Kapro is not a secret store.
- Kapro does not own cluster lifecycle; CAPI, cloud providers, Terraform, or
  platform tools do.
- Kapro does not own secret storage; use External Secrets, Vault, cloud secret
  managers, or native Kubernetes Secrets.
- Kapro does not require OCI for direct delivery.
- Kapro does not make Kargo, Sveltos, Tekton, GitLab CI/CD, GitHub Actions, or
  Kubara hard dependencies.

## First Public Preview Non-goals

- `bootstrapgen` does not upgrade previously generated repositories in
  milestone 1.

## Pipeline Substrate TODOs

ADR-0017 records the promotion-control-plane vision for GitOps, Gitless,
pipeline, platform, and custom API substrates. The pipeline-specific roadmap is:

- define the `pipeline` substrate conformance profile for correlation IDs,
  idempotent triggering, observe semantics, terminal status mapping, retryable
  failures, cancellation, and evidence;
- standardize Kapro correlation fields passed to pipeline engines: PromotionRun
  UID, target, desired version, request ID, source revision, and decision trace
  ID;
- add a common pipeline evidence shape for URLs, run IDs, job IDs, commit or
  artifact revisions, timestamps, status, logs, and artifact references;
- implement `tekton` first because it is Kubernetes-native and maps cleanly to
  typed CRDs and controller reconciliation;
- implement `gitlab-pipeline` next to prove Git-backed external pipeline
  delivery;
- evaluate GitHub Actions and custom API/webhook as the next named examples;
- leave Azure DevOps Pipelines, Buildkite, CircleCI, Jenkins, TeamCity,
  Concourse, AWS CodePipeline, Google Cloud Build, Harness, and similar systems
  to third-party `SubstrateClass` implementations until the conformance suite
  is stable.

## Partnerships And Phase 2

Kubara integration is strategically useful but must not block `0.6.0`. Treat it
as a Phase 2 partnership path:

- Kubara bootstraps the GitOps platform.
- Kapro is installed as an optional managed service in that platform.
- Kapro `bootstrapgen` may later add a Kubara-compatible output target.

## Non-goals

Kapro should not copy broad cluster-management platforms. The product center is
delivery promotion: deciding what should move, proving that it is safe to move,
and coordinating the handoff to existing delivery substrates.

The roadmap should therefore avoid generic cluster classification, inventory,
and policy-management features unless they directly improve promotion safety,
adoption, rollback, or integration authoring.
