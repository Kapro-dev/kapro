# Kapro RBAC, Multi-Tenancy, and Security Model

Kapro runs as a hub-cluster control plane for fleet promotion. The hub API is
the source of truth for promotion intent, release state, plugin registration,
and approval decisions. Spoke clusters connect outbound to the hub and reconcile
only the desired state assigned to their `MemberCluster`.

This document defines the target security architecture for Kapro deployments.

## Security Principles

- The hub cluster is the administrative trust boundary.
- All Kapro CRDs are cluster-scoped. Tenant isolation is enforced with
  Kubernetes RBAC, object naming, labels, admission policy, and release scope.
- Humans and automation write intent objects. Kapro controllers write runtime
  state and status.
- Plugins are not trusted as Kapro control-plane units. They receive
  bounded requests over a registered endpoint and return bounded decisions.
- Artifact promotion uses immutable OCI digests and signature policy. Mutable
  tags are source observations only.
- Secrets are referenced, not embedded in CRDs.
- Approval and webhook paths must be auditable, authenticated, and replay
  bounded.

## Secure-by-Default Install Posture

The production install posture is conservative:

- run the operator in a platform namespace such as `kapro-system`;
- keep admission webhooks enabled and use cert-manager or an equivalent CA
  process for serving certificates;
- use `failurePolicy: Fail` for Kapro admission webhooks after installation is
  healthy;
- keep `pluginGateway.enabled=false` unless external plugins are explicitly
  reviewed and registered;
- keep `ReleaseTrigger.spec.suspended=true` and
  `releaseTemplate.suspended=true` during onboarding;
- keep OCI `requireSignature=true` for autonomous triggers;
- install only the RBAC verbs required by each persona, using
  `examples/rbac/recommended-roles.yaml` as a starting split;
- mount approval HMAC material and notification credentials from Secrets, not
  environment variables;
- restrict egress from the operator to the Kubernetes API, approved registries,
  plugin endpoints, notification sinks, and configured webhook gates.

For install commands, see `docs/install.md`. This document defines the security
posture those installs should preserve.

## Cluster-Scoped Ownership Model

Kapro CRDs are cluster-scoped because a release pipeline can span namespaces,
regions, and clusters. Ownership is role-based:

| Resource | Primary writer | Status writer | Notes |
|---|---|---|---|
| `Kapro` | Platform administrator | Kapro operator | Hub-level installation and runtime configuration. |
| `PromotionSource` | Platform or application owner | None | Native promotion unit metadata and delivery configuration. |
| `Pipeline` | Platform administrator | None | Shared promotion template. |
| `Release` | Release engineer or trusted automation | Kapro operator | Human-created or trigger-created execution object. |
| `ReleaseTrigger` | Platform administrator or release automation owner | Kapro operator | Autonomous release creation policy. |
| `ReleaseTarget` | Kapro operator | Kapro operator | Controller-owned per-target execution state. |
| `MemberCluster` | Platform administrator or cluster onboarding automation | Hub and spoke controllers | Fleet inventory and observed cluster state. |
| `PluginRegistration` | Platform administrator | Kapro operator | External extension endpoint registration. |
| `Approval` | Approver or approval webhook | Kapro operator | Human decision signal for one release target. |
| `AgentPolicy` | Platform administrator | Admission webhook | Policy for agent identities and allowed actions. |

Users do not update `/status` subresources. The operator and admission webhook
own status, observed generation, conditions, and controller finalizers.

## Multi-Tenancy Model

Kapro supports platform-managed multi-tenancy on a shared hub:

- Platform administrators own CRDs, operator installation, `MemberCluster`,
  `Pipeline`, `PluginRegistration`, `AgentPolicy`, and trust roots.
- Application release engineers create `Release` objects against approved
  `PromotionSource` and `Pipeline` objects.
- Automation owners create `ReleaseTrigger` objects only when the artifact
  source, signature policy, release template, and release scope are approved for
  that team.
- Approvers create `Approval` objects or use the signed approval webhook for the
  targets they are authorized to approve.
- Auditors receive read-only access to specs, status, Events, and lifecycle
  notifications.

Because Kapro CRDs are cluster-scoped, Kubernetes cannot isolate tenants by
namespace alone. Production installations should combine RBAC with admission
policy that requires tenant labels such as `kapro.io/tenant`, validates allowed
pipeline names, validates allowed release scopes, and restricts secret
references to approved namespaces.

## Who Can Register Plugins

Only platform administrators register plugins.

`PluginRegistration` can change Kapro's runtime dispatch when
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`. A malicious actuator plugin can direct
deployment backends to apply an unintended version. A malicious gate plugin can
return false positive safety decisions. For that reason:

- grant create, update, patch, and delete on `pluginregistrations` only to the
  platform operator group;
- require TLS for production plugin endpoints;
- pin plugin endpoints to approved service DNS names or network locations;
- require code review and image provenance for plugin deployments;
- keep plugin Secrets in platform-owned namespaces;
- monitor `PluginRegistration.status.ready`, `status.observedGeneration`, and
  related Events.

Plugin authoring details live in `docs/plugin-authoring.md`.

## Who Can Create ReleaseTriggers

`ReleaseTrigger` creation is a platform or trusted automation-owner action.

A trigger observes an artifact source and can create `Release` objects. The
default model is safe: triggers are suspended by default, created Releases are
suspended by default, `maxActive` defaults to one, and OCI signature
verification defaults to required. Production policy should preserve that model:

- require `spec.suspended=true` during review and onboarding;
- require `spec.source.oci.requireSignature=true` unless a documented exception
  exists;
- require a restrictive `tagPattern`;
- require digest-pinned generated releases;
- require `releaseTemplate.suspended=true` for production pipelines;
- require `releaseTemplate.scope` for canary or bounded rollout triggers;
- keep `maxActive` small, normally `1`;
- restrict `source.oci.secretRef` to approved registry credential namespaces.

Release engineers may create manual `Release` objects without permission to
create `ReleaseTrigger` objects. This separation prevents a manual deployment
role from becoming an autonomous deployment role.

## Who Can Approve Gates

Only authorized approvers create `Approval` objects or use the approval webhook.

An `Approval` is the human signal for one release target approval step. It is
cluster-scoped and named deterministically from the release and approval ref.
Approver access should be narrower than release creation access:

- grant `create`, `get`, `list`, and `watch` on `approvals`;
- do not grant update, patch, delete, or `/status`;
- require the admission webhook to populate or validate `spec.approvedBy` from
  Kubernetes `UserInfo` when direct Kubernetes API approval is used;
- reserve `spec.bypass=true` for documented emergency roles;
- emit and retain Approval Events and lifecycle notifications.

The signed approval webhook is an alternative write path. It must use
short-lived, HMAC-signed tokens and should be exposed only through TLS.

## Recommended Kubernetes RBAC Roles

Use the example manifests in `examples/rbac/recommended-roles.yaml` as a
starting point. The recommended split is:

| Role | Intended subjects | Capabilities |
|---|---|---|
| `kapro-platform-admin` | Platform operator group | Manage Kapro configuration, cluster inventory, pipelines, triggers, plugins, policies, and releases. |
| `kapro-release-engineer` | Application release engineers and CI | Create and observe manual Releases. Read approved templates and inventory. |
| `kapro-release-trigger-admin` | Trusted automation owners | Manage ReleaseTriggers and read related Releases. |
| `kapro-approver` | Production approvers | Create Approval objects and observe release state. |
| `kapro-auditor` | Security and compliance readers | Read all Kapro objects and status. |
| `kapro-secret-reference-manager` | Platform secret automation | Manage referenced plugin, registry, cosign, and notification Secrets in platform namespaces. |

Bind these roles to groups, not individual users, and prefer short-lived
identity provider groups over static credentials.

## Plugin Trust Boundary

External plugins run outside Kapro's controller process. Kapro treats plugin
responses as untrusted input and keeps authority inside the controller:

- plugins do not create or mutate `Release`, `ReleaseTarget`, `Approval`, or
  `MemberCluster` state;
- plugins do not decide retry timing, failure policy, rollback policy, or stage
  fan-out;
- plugin calls are bounded by `PluginRegistration.spec.timeout`;
- only ready registrations with fresh observed generation are loaded into the
  runtime registries;
- planner plugins may influence target eligibility and ordering but do not bind
  `ReleaseTarget` objects directly;
- gate plugins return normalized phases: passed, failed, running, or
  inconclusive.

Production plugin endpoints should use mTLS or a private in-cluster network path
with server certificate verification. Plugin Pods should run with least
privilege Kubernetes RBAC for the backend they control.

## OCI and Signature Trust Model

OCI tags are discovery inputs. OCI digests are deployment inputs.

`ReleaseTrigger` observes tags that match `tagPattern`, resolves them to
immutable digests, verifies signature policy when required, and creates a
Release from the verified digest. Gate-level verification can also enforce
artifact policy before target rollout.

The recommended production model is:

- CI builds an artifact and signs the pushed digest.
- Kapro resolves matching tags to digests before creating a Release.
- Signature verification uses either keyless issuer and subject constraints or
  a cosign public key stored in a Kubernetes Secret.
- Unsigned or unverifiable artifacts do not create autonomous Releases when
  `requireSignature=true`.
- Promotion status records the observed digest and verification result.

Registry credentials and cosign keys are Secrets. CRDs reference those Secrets
by name and namespace; they do not store credential material.

Example manifests for suspended, digest-pinned ReleaseTrigger use with cosign
keyless verification are available in `examples/release-trigger/`.

## Webhook and Gate Security

Admission webhooks protect Kapro's API invariants. They should run with TLS,
validated CA bundles, and a failure policy selected for the environment's risk
tolerance. Production hubs should use `failurePolicy: Fail` for Kapro admission
once rollout is complete.

Gate webhooks and notification webhooks are outbound calls from Kapro. Treat
them as external systems:

- use HTTPS URLs;
- prefer private service DNS names for in-cluster policy services;
- do not send Secrets in gate parameters;
- bound polling intervals and gate timeouts;
- treat webhook responses as gate input, not authoritative release state;
- capture gate pass and fail Events for audit.

Approval webhooks are inbound decision paths. They must validate signed tokens,
bind the token to release, target, ref, action, and expiration, and reject
replay outside the token validity window.

## Secret Handling

Kapro references Secrets for registry credentials, plugin TLS, cosign public
keys, SMTP credentials, approval HMAC material, and bootstrap output.

Secret handling rules:

- never put credential values in CRD specs, annotations, labels, Events, or
  logs;
- keep referenced Secrets in platform-owned namespaces such as `kapro-system`;
- grant Secret read only to the Kapro operator service account and the narrow
  automation that owns those Secrets;
- do not grant broad Secret read to release engineers, trigger owners, plugin
  authors, or approvers;
- prefer External Secrets Operator, sealed secrets, or cloud secret managers for
  Secret lifecycle;
- rotate approval HMAC and plugin TLS material on a defined schedule;
- use namespace-qualified Secret references for cluster-scoped objects.

## Threat Model

| Threat | Control |
|---|---|
| Untrusted user registers a malicious actuator or gate plugin. | Restrict `PluginRegistration` writes to platform administrators; require endpoint allowlists, TLS, and plugin deployment review. |
| Release engineer creates an autonomous trigger that deploys every pushed tag. | Separate `Release` and `ReleaseTrigger` roles; require admission policy for `tagPattern`, `requireSignature`, `maxActive`, and `releaseTemplate.scope`. |
| Approver approves the wrong target or bypasses required gates. | Scope approver groups, use deterministic approval refs, audit `spec.approvedBy`, and reserve bypass for emergency roles. |
| Mutable tag is retargeted after approval. | Resolve tags to immutable OCI digests and promote only digests. |
| Unsigned artifact is promoted by automation. | Keep `requireSignature=true`; configure cosign keyless or key-based verification; block unverifiable observations. |
| Plugin endpoint returns false gate success. | Treat plugins as untrusted, restrict registration, monitor plugin readiness, and use independent verification gates for high-risk stages. |
| Secret leaks through CRD specs or Events. | Store only Secret references in CRDs; restrict Secret RBAC; avoid logging Secret values. |
| Compromised spoke cluster writes another cluster's status. | Bind spoke identity to one `MemberCluster`; use AgentPolicy and RBAC so the identity can read and patch only its allowed cluster object. |
| Approval webhook token is replayed. | Use short expirations, HMAC signing, action-bound claims, and deterministic Approval object names. |
| Admission webhook outage blocks emergency operations. | Run multiple replicas, manage CA rotation, monitor webhook health, and document the operational break-glass process. |
