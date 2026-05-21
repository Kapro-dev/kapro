# Security Model

Kapro is a promotion control plane. It can cause production changes across many
clusters, so the security model assumes that Promotion updates, PromotionRun
attempt creation, `Plugin` changes, approval, artifact verification, and
webhook gates are privileged operations.

For role design, see `docs/rbac-tenancy.md`.

## Decision API Authentication

The machine-facing Decision API is opt-in and must not be exposed without
Kubernetes authentication and authorization. When enabled, each `/api/v1`
request must include a bearer token. Kapro validates it with Kubernetes
`TokenReview` and authorizes every read or mutation with
`SubjectAccessReview`.

The authenticated Kubernetes username is the audit identity stored on decision
traces and human overrides. Request payload fields such as `identity` are not
trusted as the source of authority.

Decision API list responses are bounded by default and support `limit`,
`labelSelector`, and `phase` query parameters. Clients must treat
`page.truncated=true` as a partial view and issue narrower follow-up queries
instead of assuming the response contains the whole fleet. Sparse filtered reads
also stop at a server-side scan budget and return `page.truncated=true`.

Grant narrow RBAC:

- read-only agents may `get`/`list` `PromotionRun`, `Target`, and
  `Cluster` objects;
- decision agents may also `update` `targets/status`;
- agents that can approve must separately be able to `create` `approvals`;
- override access should be bound to a different emergency role.

## Threat Model

| Threat | Mitigation |
|---|---|
| Untrusted artifact triggers an automatic Promotion | Digest pinning, signature verification, suspended-by-default triggers and Promotions |
| Compromised plugin unblocks or mutates production | Restricted `Plugin` RBAC, TLS/mTLS, short timeouts, narrow KAI/KGI/KPI contracts |
| User approves a gate outside their team or environment | Admission policy on `Approval` labels, request user info, and bypass use |
| Webhook gate is spoofed or replayed | HTTPS, shared secret or mTLS at the webhook backend, idempotent decision refs |
| Registry credential leaks | Namespaced Secret refs, least-privilege registry tokens, operator-only Secret reads |
| Controller compromise spreads across clusters | Minimal operator RBAC, separate hub/spoke credentials, bounded actuator permissions |
| Status tampering hides a failed rollout | Status subresources are controller-owned; users receive read-only status access |

## Plugin Trust Boundary

External plugins are outside the Kapro trust boundary. Kapro sends bounded
requests over the registered protocol and treats plugin responses as advisory
backend results, not as ownership of PromotionRun state.

Plugins must not:

- create or mutate `Target` objects;
- change `PromotionRun.status`;
- bypass Kapro retries, timeouts, or failure policy;
- store irreplaceable PromotionRun state only in plugin memory;
- require cluster-admin credentials for ordinary gate or actuator work.

Plugins should:

- implement TLS and, for production, mTLS;
- run in platform-controlled namespaces;
- use least-privilege service accounts for backend access;
- return deterministic decisions for identical inputs;
- respect context cancellation and configured timeouts;
- emit their own audit logs for backend changes.

## OCI and Signature Trust Model

`Trigger` is conservative by default:

- `spec.suspended` defaults to `true`;
- generated Promotions default to suspended, and stamped PromotionRuns inherit
  that suspension;
- OCI signature verification defaults to off until a trigger verifier is
  configured, and fails closed when explicitly required without a verifier;
- generated Promotions and stamped PromotionRuns should use immutable digests,
  not mutable tags;
- cooldown and max-active limits reduce attempt floods.

The intended production posture is:

1. CI publishes an OCI artifact and signs it.
2. Kapro observes only tags that match the trigger pattern.
3. Kapro resolves the tag to an immutable digest.
4. If `requireSignature: true` is set and a verifier is configured, Kapro
   verifies signature policy before Promotion creation. Without a configured
   verifier, the trigger fails closed with `VerifierUnavailable`.
5. Kapro creates or updates a suspended, digest-pinned Promotion.
6. A Promotion manager reviews and unsuspends the Promotion or trigger according to
   environment policy.

Keyless verification should pin expected issuer and subject identity. Key-based
verification should use a trusted public key distributed through a
platform-owned Secret or ConfigMap. Unsigned artifacts must not create automatic
production PromotionRuns.

### Trigger with cosign keyless policy

`Trigger` observes tags and creates or updates a digest-pinned Promotion. Set
`requireSignature: true` only after installing a verifier implementation for
the trigger controller; otherwise the trigger intentionally blocks.

```yaml
apiVersion: kapro.io/v1alpha2
kind: Plan
metadata:
  name: checkout-keyless
spec:
  stages:
    - name: canary
      selector:
        matchLabels:
          kapro.io/tier: canary
      gate:
        mode: auto
---
apiVersion: kapro.io/v1alpha2
kind: Trigger
metadata:
  name: checkout-oci-keyless
spec:
  suspended: true
  source:
    type: oci
    oci:
      repository: oci://registry.example.com/platform/checkout
      tagPattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$"
      requireSignature: true
      pollInterval: 5m
  promotionTemplate:
    fleetRef: checkout
    plans:
      - name: production
        plan: checkout-keyless
    suspended: true
    scope:
      targets:
        - checkout-canary-eu
  cooldown: 30m
  maxActive: 1
  dryRun: true
```

### Cosign public key material

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: checkout-cosign-public-key
  namespace: kapro-system
type: Opaque
data:
  cosign.pub: <base64-encoded-public-key>
---
apiVersion: kapro.io/v1alpha2
kind: Trigger
metadata:
  name: checkout-oci-keyed
spec:
  suspended: true
  source:
    type: oci
    oci:
      repository: oci://registry.example.com/platform/checkout
      tagPattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$"
      requireSignature: true
  promotionTemplate:
    fleetRef: checkout
    plans:
      - name: production
        plan: checkout
    suspended: true
```

## Webhook and Gate Security

Webhook gates call external systems to decide whether a target may advance.
They should be treated as production policy endpoints.

Requirements:

- use HTTPS for all non-development webhook endpoints;
- authenticate requests with mTLS or a shared secret;
- validate request timestamp or nonce when the backend supports it;
- make decisions idempotent for a PromotionRun, stage, target, and gate ref;
- return a bounded response containing only the normalized gate result and
  operator-facing message;
- avoid embedding credentials in gate parameters.

Human approvals are also gates. The approval admission path should set
`spec.approvedBy` from the authenticated request identity and restrict
`spec.bypass` to emergency groups.

## Secret Handling

Kapro references Secrets for registry credentials, plugin TLS, approval tokens,
and notification providers.

Rules:

- all Secret refs from cluster-scoped objects must include `namespace`;
- Secrets should live in platform-controlled namespaces unless a team-specific
  credential is intentionally delegated;
- the operator service account should read only the Secret names and namespaces
  required by enabled features;
- plugin credentials should be mounted into plugin pods, not copied into
  `Plugin.spec.parameters`;
- never write credential values into status, Events, logs, or notifications.

Rotate registry and plugin credentials independently from PromotionRun state. A
credential rotation should not require recreating PromotionRun or Plan objects.

## Audit Evidence

Kapro records status, Kubernetes Events, and optional lifecycle notifications.
For regulated environments, send lifecycle notifications to an append-only
external sink and retain:

- artifact digest and signature verification result;
- PromotionRun and Trigger object metadata;
- gate result and message;
- approver identity and bypass flag;
- plugin name, version, and endpoint identity;
- target phase transitions and failure reasons.
