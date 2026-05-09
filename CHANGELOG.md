# Changelog

## v0.1.0 — Initial Release

First release of Kapro: a Kubernetes-native progressive delivery engine for
multi-cluster fleet rollouts. Built on Flux, OCI-first, outbound-only spoke
connectivity, composable gates, and horizontal scaling via sharding.

### CRDs (7)

- **Artifact** — immutable OCI bundle reference, digest-pinned, created by CI or auto-discovered
- **Pipeline** — reusable progressive delivery DAG of stages with label selectors (template only, no controller)
- **Release** — trigger for rollout across cluster fleet; references Artifacts and Pipelines
- **ReleaseTarget** — per-target execution state within a Release (child object, controller-driven FSM)
- **MemberCluster** — fleet cluster inventory with delivery config, registration state, and bootstrap credential
- **Approval** — human gate signal to unblock a waiting target rollout
- **Source** — OCI registry watcher; auto-discovers semver tags and creates Artifact objects

### Controllers (5)

- **ReleaseReconciler** — orchestrates two-level DAG: pipeline dependencies → stage dependencies → parallel target expansion
- **ReleaseTargetReconciler** — advances per-target FSM independently (Pending → Verification → HealthCheck → Soaking → MetricsCheck → WaitingApproval → Applying → Converged/Failed/Skipped)
- **SourceReconciler** — polls OCI registries, discovers new tags matching semver constraints
- **ApprovalReconciler** — audit trail via immutable Events
- **CSRApprovalReconciler** — handles bootstrap CSR approval during cluster registration

### Actuator Framework (KAI Interface)

- **Flux actuator** — CRD-native outbound pattern; patches MemberCluster.spec.desiredVersions on hub; cluster-controller polls and patches local Flux OCIRepository
- Interface: Apply, ApplyDelta (multi-artifact), IsConverged, IsAllConverged, Rollback

### Gates (8 built-in)

- **Soak** — minimum duration elapsed before advancing
- **Approval** — blocks until Approval CR exists; supports P0 hotfix bypass
- **Metrics** — Prometheus PromQL evaluation with configurable window, interval, threshold
- **HealthCheck** — active health polling via configurable endpoint
- **CEL** — inline CEL expression evaluation with target context
- **Job** — Kubernetes Job; exit 0 = pass, non-zero = fail
- **Webhook** — HTTP callback; blocks forbidden IPs (loopback, private, link-local)
- **Verification** — cosign signature verification (keyless OIDC + static key)

### Progressive Delivery

- 10-state per-target FSM with durable phase persistence
- Stage failure policies: **halt** (default), **skip**, **rollback**
- Gate failure policies: **halt** (default), **rollback**, **continue** (mark skipped)
- Global Release timeout with automatic failure on expiry
- Multi-artifact delta delivery — only changed artifacts are pushed; inherited artifacts merged from parent
- Stage label selectors — add clusters to a wave by labeling, no Pipeline changes needed

### Cluster Management

- Lease-based heartbeat (coordination.k8s.io/v1) — reduced hub API pressure vs status writes
- Heartbeat staleness tracking with configurable fail timeout (10m default)
- Bootstrap via one-time token hash (SHA-256) + CSR proof-of-identity
- Generic (Kubernetes CSR) and GCP (Workload Identity) bootstrap providers
- Self-reported capabilities: k8s version, Flux version, cloud, region, GPU topology

### Scalability (1000+ clusters)

- Controller sharding via `KAPRO_SHARD` env + `kapro.io/shard` label predicate
- Conditional poll — ReleaseTargetReconciler skips reconcile when no work needed
- Field-indexed ReleaseTarget lookups — O(1) release→targets, no full scans
- Incremental persistReleaseTargets — skips API writes when spec unchanged

### Webhook Server

- Approval endpoints: POST /approve, POST /reject, GET /status
- HMAC-SHA256 token verification scoped per Release UID + target key (48h TTL)
- Healthz probe at GET /healthz

### Notifications

- Built-in: Slack (zero deps), webhook (zero deps)
- Rich engine: 15+ providers via argoproj/notifications-engine (Teams, PagerDuty, OpsGenie, email SMTP, etc.)
- Gate-level notifications to approvers on WaitingApproval entry

### CLI (`kapro`)

- `kapro cluster bootstrap` / `kapro cluster join` — register spoke clusters
- `kapro get releases` / `kapro get targets` — observe rollout state
- `kapro approve` / `kapro reject` — gate management
- `kapro rollback` — trigger rollback to previous digest
- `kapro release create` / `kapro artifact push` / `kapro promote`
- `kapro world` — global fleet delivery view
- `kapro spoke install` — Flux-aligned declarative spoke installer

### Admission Webhooks

- ValidatingWebhook: Pipeline DAG cycle detection, Release artifact/pipeline ref validation
- MutatingWebhook: Approval defaults (ApprovedBy from request UserInfo), Release env injection

### Executables

- **kapro-operator** — hub control plane (controllers + webhooks + approval server)
- **kapro-cluster-controller** — spoke agent (one per workload cluster)
- **kapro** — CLI for operators and CI

### What's NOT in v0.1

- Decision API (REST endpoints for AI-driven approvals) — planned for v0.3
- AgentPolicy CRD (AI trust boundaries) — planned for v0.4
- DecisionTrace (structured reasoning on ReleaseTarget status) — planned for v0.3
- ArgoCD actuator
- Sveltos actuator
