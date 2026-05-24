# Industry Requirements for Kapro GA

This document maps current cloud-native delivery requirements to Kapro's
roadmap. It is intentionally opinionated: Kapro should meet these before a
`1.0.0` GA claim, and should avoid shipping lookalike platform-management
features unless they directly support promotion safety.

## Source Baseline

Primary standards and papers used for this baseline:

| Source | Kapro implication |
| --- | --- |
| [NIST SP 800-218 SSDF](https://csrc.nist.gov/pubs/sp/800/218/final) | Treat secure development as a lifecycle practice: documented secure design, code review, vulnerability handling, release integrity, and supplier communication. |
| [SLSA security levels](https://slsa.dev/spec/v1.0/levels) and [SLSA source track v1.2](https://slsa.dev/spec/v1.2/source-requirements) | Ship provenance, signed releases, hosted/hardened builds, and source-control protections with review expectations. |
| [CNCF Software Supply Chain Security Paper](https://contribute.cncf.io/projects/best-practices/security/supply-chain/supply-chain-security-paper/) | Cover build, distribution, deployment, and runtime supply-chain controls as a layered system. |
| [in-toto, USENIX Security 2019](https://www.usenix.org/conference/usenixsecurity19/presentation/torres-arias) | Use cryptographic attestations for end-to-end supply-chain integrity, not only SBOM files. |
| [NIST SP 800-190 Container Security](https://csrc.nist.gov/pubs/sp/800/190/final) | Harden container images, runtime isolation, vulnerability scanning, secrets, and orchestration controls. |
| [NSA/CISA Kubernetes Hardening Guidance](https://kubernetes.io/blog/2021/10/05/nsa-cisa-kubernetes-hardening-guidance/) | Address supply-chain risk, malicious actors, insider threats, least privilege, network separation, logging, and periodic reviews. |
| [Kubernetes Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/) | Default operator and spoke workloads should satisfy Baseline, with a path to Restricted where practical. |
| [OpenSSF Scorecard](https://github.com/ossf/scorecard) | Track open-source hygiene: branch protection, code review, pinned dependencies, SAST, security policy, token permissions, vulnerability handling, and signed releases. |
| [Borg cluster-management paper](https://research.google.com/pubs/pub43438.html) | Production fleet systems need admission control, fault-domain awareness, runtime monitoring, and tools to analyze/simulate behavior. |
| [Dapper tracing paper](https://research.google/pubs/dapper-a-large-scale-distributed-systems-tracing-infrastructure/) | Explainability must be ubiquitous, low overhead, and useful to both developers and operators. |

## Final GA Requirements

Kapro should not claim `1.0.0` until these are true.

### 1. Supply Chain Integrity

- Release artifacts have SLSA provenance and signed attestations.
- Containers and binaries are signed, and verification instructions are public.
- SBOMs are published in SPDX or CycloneDX form.
- Release automation fails if provenance, signatures, SBOMs, or changelog entries are missing.
- Dependency update automation and vulnerability scanning run continuously.
- CI uses least-privilege tokens and pinned third-party actions where possible.

### 2. Secure Development Practice

- `SECURITY.md` documents vulnerability reporting and response expectations.
- Public API stability and deprecation policy are explicit.
- All public CRDs avoid dead fields at first release.
- Code review, generated-code drift checks, YAML validation, markdown link checks, and Go tests are required before merge.
- `golangci-lint` or equivalent hardening is enforced for formatting, static analysis, error handling, and security-sensitive patterns.

### 3. Kubernetes Runtime Hardening

- Helm defaults set restrictive security contexts for all Kapro pods.
- Workloads avoid privileged mode, host namespaces, writable root filesystems, and unnecessary Linux capabilities.
- Pod Security Standards are documented and tested: Baseline required, Restricted targeted where practical.
- RBAC is least privilege and resource-name constrained where feasible.
- NetworkPolicy examples exist for default-deny operation.
- Secrets are never logged and are scoped to the minimum namespace and service account.

### 4. Promotion Auditability

- Every gate, approval, batch decision, rollback, suspend, and staging decision emits a durable `DecisionTrace`.
- Decision traces can be archived outside the cluster.
- `kapro why <promotion>` reconstructs the decision path without reading controller logs.
- Traces include enough identity, policy, version, target, and result data for incident review.
- Optional signing is available for trace integrity.

### 5. Plugin and Substrate Contract

- Actuator, gate, planner, and provider plugin contracts are versioned and conformance-tested.
- `kapro-conformance` can test external plugin endpoints in CI.
- At least two external substrates pass conformance before GA.
- Built-in Argo CD and Flux paths are dogfooded through the same capability model as external plugins.
- Capability bits make unsupported operations explicit before runtime calls.

### 6. Drift and Atomicity

- Kapro can report desired vs actual state across supported substrates.
- Drift duration and count are exported as metrics.
- A `MaxDrift` gate can block promotions when drift exceeds policy.
- Two-phase staging supports per-cluster prepare, commit, and discard.
- Multi-artifact changes on one cluster can be committed atomically when the substrate supports it.

### 7. Bootstrap and Identity

- Spoke bootstrap uses short-lived material and per-cluster identity.
- Per-cluster RBAC limits each spoke to its own resources where Kubernetes supports it.
- Outbound-only spoke operation is supported.
- Heartbeats use leases and consecutive-failure hysteresis, not single missed checks.
- Air-gap export/import paths are signed and auditable.

### 8. Operational Readiness

- Metrics cover controller health, reconciliation latency, promotion outcomes, gate outcomes, drift, retention, and archive delivery.
- SLO dashboards and alert examples are shipped.
- Upgrade and rollback procedures are documented and tested.
- E2E smoke tests cover install, Argo, Flux, archive, and plugin-conformance paths.
- The operator has scale tests for large numbers of clusters, promotions, targets, gates, and archived events.

## Roadmap Order

The practical sequence is:

1. Fix public v0.2.x surfaces: AdapterPolicy, Gate API docs, DELAY semantics, server composition.
2. Finish the plugin keystone: versioned actuator/provider contracts and conformance.
3. Add auditability: `DecisionTrace`, archive integration, and `kapro why`.
4. Add drift and atomicity later: first ship two-phase staging and keep fleet drift reporting as post-0.6 work.
5. Add bootstrap/air-gap identity: CSR bootstrap, outbound spoke, signed bundles.
6. Add fleet classification only after drift and provider discovery are real.
7. Add compliance/release trust: SLSA, SBOM, signed releases, Scorecard hardening.

## ClusterClassifier Positioning

`ClusterClassifier` should not be the next flagship feature. It is only justified
after provider discovery, drift, and substrate capability reporting exist. When
it does ship, it should be framed as promotion-safe routing:

- derive Kapro-owned labels from provider inventory, drift, bootstrap posture,
  substrate capabilities, and readiness signals;
- never overwrite user-owned labels;
- expose conflicts explicitly;
- support dry-run preview before mutation.

This keeps Kapro differentiated from general cluster-management systems. The
classification feature supports promotion safety; it is not the product.
