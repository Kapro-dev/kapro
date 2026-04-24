# ADR-001: Should Kapro Be Submitted to CNCF Sandbox?

**Status:** Proposed  
**Date:** 2026-04-19  
**Deciders:** Vinayaka Krishnamurthy (@vinnxcapital-gif)  
**Reviewed by:** Architecture analysis (Claude / Cowork)

---

## Context

Kapro is a Kubernetes-native progressive delivery engine that positions itself as the "canonical promotion layer" — the horizontal wave layer between pre-production stage tools (Kargo) and delivery tools (Flux / ArgoCD). The question is whether the project's architecture, maturity, and design philosophy justify applying to the [CNCF Sandbox](https://www.cncf.io/sandbox-projects/).

The CNCF Sandbox is the entry tier for early-stage, cloud-native projects that are open source, have a credible value proposition, and commit to CNCF governance. It does **not** require production adoption at application time — that is a Incubating-stage requirement.

---

## The Problem Kapro Solves

The current progressive delivery landscape has a real gap:

```
[ CI ] → [ Kargo ] → ??? → [ Flux / ArgoCD ] → [ Cluster ]
          pre-prod            delivery
          stage gates         per-cluster
```

Kargo manages pre-production stage gates (dev → staging → QA). Flux and ArgoCD deliver to individual clusters. Neither solves **wave-based, multi-cluster production promotion** — releasing version v1.2.4 to 3 "pilot" clusters, waiting for soak time and metrics gates, then rolling it out to 50 production clusters across regions.

Kapro fills this exact gap. This is not a theoretical problem — it is a common operational pain point at organizations running Kubernetes at scale (multi-region, multi-cloud, or multi-tenant platforms).

---

## Architecture Assessment

### Layer Design

Kapro implements a clean three-layer abstraction:

| Layer | CRDs | Role |
|-------|------|------|
| **Artifact** | `Artifact` | Immutable, digest-pinned OCI bundle |
| **Topology** | `Target`, `TargetGroup`, `ClusterRegistration` | Where versions go |
| **Strategy** | `Pipeline`, `Release`, `Approval` | How versions move |

This separation is architecturally sound and mirrors established Kubernetes patterns. Each layer has a single, clear concern.

### KSI — Kapro Standard Interfaces

Kapro exposes two stable, pluggable extension interfaces today, modelled on the Kubernetes CRI/CSI/CNI pattern:

| KSI | Interface      | Abstracts                          |
|-----|----------------|------------------------------------|
| KAI | `pkg/actuator` | How a version is applied to a cluster |
| KGI | `pkg/gate`     | Whether it is safe to advance a rollout |

Both are backed by a named `Registry[T]` (`pkg/registry`) and validated by a conformance suite in `conformance/`. A Python, Rust, or Go team can ship a custom actuator or gate and register it at operator startup by name.

Built-in implementations:
- **Actuators**: Flux (reference implementation)
- **Gates**: Soak timer, Prometheus metrics, cosign signature verification, human approval, plus template-dispatch gates (`cel`, `job`, `webhook`)

Cluster onboarding (CSR + optional GCP Workload Identity Federation) and cluster inventory (`MemberCluster`) are deliberately **not** pluggable extension points — they live in `internal/bootstrap` and the `MemberCluster` CRD respectively. Past iterations included a generic cluster-provider interface (KCI); it has been removed in favour of the simpler `MemberCluster` object.

### State Machines

All core objects are driven by explicit state machines:

- `Release`: `Pending → Promoting → Progressing → Complete | Failed`
- Per-target (inline in `Release.status.targets[].phase`): `Pending → Verification → HealthCheck → MetricsCheck → Applying → Converged | Failed`

Stage promotion is handled by the Pipeline stage chain (`dependsOn` DAG). Each stage selects matching clusters and fans out rollout in parallel — per-cluster execution state lives inline in `Release.status.targets[]`. Progressive promotion is the stage-chain itself: dev must reach Converged before staging is started, staging before prod. No separate `Promotion`, `BatchRun`, or `Sync` CRDs — the `Release → Pipeline → Stage → target-status` model covers both concepts natively.

State is stored in Kubernetes (etcd). Controllers are stateless and crash-safe. Every transition is observable via `kubectl get releases` and `kubectl describe release <name>`.

### Multi-cluster Connectivity Model

Kapro's cluster connectivity is security-conscious by design:
- One lightweight `kapro-cluster-controller` pod per workload cluster
- **Outbound-only** HTTPS to control plane — no inbound firewall holes required
- OIDC/Workload Identity for auth — no static credentials
- CRDs as the single cross-cluster communication channel

This is the correct security model for enterprise environments. It is meaningfully different from approaches that require bi-directional trust or central credentials.

### GPU / ML Workload Support

The `EnvironmentTopology` spec (accelerator type, GPU count, GPU memory, node count) indicates Kapro is designed for AI/ML workload promotion — a rapidly growing use case in cloud-native infrastructure. The MLflow gate and KServe actuator reinforce this. This is a strong differentiator and relevant to CNCF's current growth areas.

### Plugin System

External plugins implement KSI interfaces over gRPC and self-register via a `PluginRegistration` CRD:

```yaml
apiVersion: kapro.io/v1alpha1
kind: PluginRegistration
spec:
  type: Actuator
  endpoint: grpc://pulumi-actuator-svc:9090
```

This is a clean, Kubernetes-native extensibility model that avoids the pitfalls of webhook-only or binary plugin systems.

### Technical Stack

- Go 1.25, `sigs.k8s.io/controller-runtime v0.23`, `k8s.io/api v0.35` — on the latest toolchain
- `oras.land/oras-go/v2` for OCI operations — the CNCF-standard OCI library
- `sigstore/cosign v2` for supply chain verification
- `google/cel-go` for CEL-based policy expressions
- React UI with `kubectl`-first observability philosophy
- Helm charts for both control plane and cluster controller
- `buf` for proto schema management
- Multi-arch Docker builds (amd64 / arm64)

---

## Options Considered

### Option A: Submit to CNCF Sandbox Now

| Dimension | Assessment |
|-----------|------------|
| Architectural readiness | **High** — clean design, strong differentiation |
| API maturity | **Medium** — v1alpha1, well-structured but not frozen |
| Test coverage | **Low** — ~11% (11 test files for 103 Go files) |
| Community | **Low** — 1 maintainer, no documented adopters |
| CNCF fit | **High** — cloud-native, Kubernetes-native, fills real gap |
| Governance readiness | **Medium** — has CoC, CONTRIBUTING, SECURITY, MAINTAINERS; missing GOVERNANCE.md |
| Overlap with existing CNCF projects | **Low risk** — orthogonal to Flux, ArgoCD, Kargo |

**Pros:**
- Early CNCF visibility accelerates community growth
- Signals commitment to open governance, attracting contributors
- Access to CNCF infrastructure (artifact hosting, CI, LFX tools)
- Positions Kapro in CNCF's progressive delivery landscape before a competitor fills the niche
- Architecture is strong enough to withstand TOC scrutiny

**Cons:**
- TOC due diligence will surface the low test coverage — may result in conditional acceptance or deferral
- Single maintainer is a red flag for CNCF TOC; they expect at least 2–3 active contributors
- No evidence of production adoption weakens the application narrative
- `v1alpha1` API may signal instability to evaluators

---

### Option B: Strengthen First, Then Submit (3–6 Months)

Spend 3–6 months building the project's community and operational readiness before applying.

| Dimension | Assessment |
|-----------|------------|
| Architectural readiness | **High** (unchanged) |
| Test coverage target | **60%+** achievable in this window |
| Community target | 2–3 additional maintainers, 1–2 early adopters |
| Governance | Complete GOVERNANCE.md, regular community meetings |
| Overlap risk | A competitor could enter the space during this window |

**Pros:**
- Much stronger application — fewer conditions from TOC
- Test coverage and observability gaps are resolved before scrutiny
- Multiple maintainers signals sustainability
- Real-world adoption evidence strengthens the "why CNCF" narrative

**Cons:**
- 3–6 months delay; window to define the standard could close
- Community growth is harder before CNCF affiliation provides visibility
- Requires disciplined parallel effort (community + code)

---

### Option C: Do Not Submit — Remain Independent

Grow Kapro as an independent OSS project and revisit CNCF at the Incubating stage.

**Pros:** Full autonomy, no governance overhead  
**Cons:** Lower discoverability, no CNCF infrastructure, harder to attract contributors, harder to compete with CNCF-backed tools in enterprise evaluations

This option is not recommended given the project's explicit cloud-native positioning and its reliance on CNCF-ecosystem integrations (Flux, ArgoCD, CAPI, OCM, KEDA, Sigstore).

---

## Trade-off Analysis

The core tension is **timing vs. readiness**. CNCF Sandbox is explicitly designed for early-stage projects — it is not an endorsement of production-readiness, it is an endorsement of *direction*. The TOC evaluates whether the project addresses a real problem, has a credible architecture, and is committed to open governance. On all three counts, Kapro qualifies.

The gaps (test coverage, single maintainer, no documented adopters) are addressable in parallel with a sandbox application. Many successful sandbox projects (Kargo, for example) applied with similar community profiles. The differentiator that matters most to TOC is **"does this overlap with an existing CNCF project?"** — and Kapro's analysis shows it does not meaningfully overlap with Flux, Kargo, Flagger, or ArgoCD.

The KSI interface architecture is the strongest argument for Sandbox acceptance: it is the kind of ecosystemic thinking — stable, extensible, language-agnostic contracts — that CNCF exists to promote.

---

## Decision

> **Proceed with CNCF Sandbox application, in parallel with targeted pre-submission hardening over 6–8 weeks.**

This is a hybrid of Options A and B. Submit the application with full transparency about the project's current state and a concrete roadmap to address gaps. CNCF TOC regularly accepts projects with this profile when the architecture and value proposition are compelling.

**Rationale:**
1. The problem is real and unaddressed in the CNCF landscape.
2. The KSI interface system is a genuine architectural contribution — not just another tool.
3. The security model (outbound-only, OIDC, CRD-based cross-cluster comms) is enterprise-appropriate.
4. GPU/ML topology awareness is timely and differentiating.
5. The technology choices (controller-runtime, oras-go, cosign, CEL) are all CNCF-ecosystem-native.
6. Waiting risks allowing a competing project to establish the standard.

---

## Pre-Submission Requirements (6–8 Weeks)

These are the minimum changes needed before filing the CNCF Due Diligence document.

### Mandatory (Blockers)

- [ ] **Add a second maintainer** — CNCF TOC will not accept a single-maintainer project; recruit at least one external contributor and promote them
- [ ] **Write GOVERNANCE.md** — document decision-making process, maintainer ladder, voting, conflict resolution
- [ ] **Increase test coverage to ≥40%** — focus on `release_controller`, `sync_controller`, `pipeline_controller`
- [ ] **Add Prometheus metrics** — expose promotion state, gate evaluation results, and wave progress as metrics; this is expected of any CNCF-ecosystem tool
- [ ] **Document at least one real-world usage scenario** — even a controlled POC at a real organization (can be the submitter's own) demonstrates viability

### Strongly Recommended

- [ ] **Structured logging with correlation IDs** — correlate Release → Pipeline → Stage → Sync across log lines
- [ ] **Health check / readiness endpoints** — required for production Kubernetes deployments
- [ ] **Tag a v0.1.0 release** — gives the project a concrete version anchor for due diligence
- [ ] **Add a ROADMAP.md** — shows TOC the project has a vision beyond current scope
- [ ] **Expand conformance test suite** — the `conformance/` directory exists; flesh it out; this signals interface stability

### Differentiators to Highlight in the Application

- [ ] **KSI interface specification** — publish the 7 interface contracts as a formal spec (similar to how CNI/CSI specs are documented)
- [ ] **GPU-aware promotion topology** — highlight as a unique capability for AI/ML platform teams
- [ ] **gRPC plugin system** — frame this as "Kapro's CNI moment" for promotion tooling

---

## Consequences

**What becomes easier after CNCF Sandbox acceptance:**
- Attracting contributors — CNCF affiliation is a strong signal for engineers evaluating open-source projects to contribute to
- Enterprise adoption — many enterprise platform teams require CNCF affiliation before adopting infrastructure tooling
- Integration partnerships — Flux, Kargo, CAPI, and OCM maintainers are more likely to collaborate on integration work
- Infrastructure — CNCF provides CI/CD infrastructure, artifact hosting, and LFX tooling at no cost

**What becomes harder:**
- API changes require adherence to CNCF/Kubernetes API deprecation policy (v1alpha1 → v1beta1 → v1 pathway)
- Governance overhead — community meetings, TOC reports, security disclosure processes
- External contributors may push the project in directions that differ from the original vision

**What will need revisiting at Incubating stage:**
- Documented production adopters (minimum 3 required for Incubating)
- Multiple maintainers from multiple organizations
- Stable v1beta1 API surface
- Security audit (CNCF provides funding for this at Incubating)

---

## Landscape Comparison

| Project | CNCF Status | Scope | Overlaps with Kapro? |
|---------|-------------|-------|----------------------|
| Flux | Graduated | GitOps delivery to one cluster | No — Kapro orchestrates Flux |
| ArgoCD | Graduated | GitOps delivery to one cluster | No — Kapro is an ArgoCD actuator |
| Kargo | Sandbox | Pre-prod stage gates (dev→staging→QA) | Minimal — Kargo is upstream input to Kapro |
| Flagger | Sandbox | Canary / traffic-split within one cluster | No — different promotion scope |
| Argo Rollouts | Incubating | Canary / blue-green within one cluster | No — single cluster, different concern |
| **Kapro** | *Proposed Sandbox* | Multi-cluster wave promotion for production | Fills the gap between all of the above |

---

## Final Verdict

**Yes — Kapro is worth submitting to CNCF Sandbox.**

The architecture is genuinely novel, the problem is unaddressed in the CNCF landscape, and the KSI interface system represents the kind of ecosystem-building that CNCF exists to support. The gaps are real but not disqualifying for Sandbox tier. Address the mandatory pre-submission items above, recruit one additional maintainer, and file the application.

The risk of waiting — allowing another project to define the multi-cluster promotion standard — outweighs the risk of a conditional acceptance from TOC.

---

*Document generated: 2026-04-19*  
*Next review: Before filing CNCF Due Diligence document*
