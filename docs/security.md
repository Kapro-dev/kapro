# Security Model

Kapro is a promotion control plane. It can cause production changes across many
clusters, so the security model assumes that promotionrun creation, plugin
registration, approval, artifact verification, and webhook gates are privileged
operations.

For role design, see `docs/rbac-tenancy.md`. For the full control-plane trust
model, see `docs/security-model.md`.

## Threat Model

| Threat | Mitigation |
|---|---|
| Untrusted artifact triggers an automatic promotionrun | Digest pinning, signature verification, suspended-by-default triggers and PromotionRuns |
| Compromised plugin unblocks or mutates production | Restricted `PluginRegistration` RBAC, TLS/mTLS, short timeouts, narrow KAI/KGI/KPI contracts |
| User approves a gate outside their team or environment | Admission policy on `Approval` labels, request user info, and bypass use |
| Webhook gate is spoofed or replayed | HTTPS, shared secret or mTLS at the webhook backend, idempotent decision refs |
| Registry credential leaks | Namespaced Secret refs, least-privilege registry tokens, operator-only Secret reads |
| Controller compromise spreads across clusters | Minimal operator RBAC, separate hub/spoke credentials, bounded actuator permissions |
| Status tampering hides a failed rollout | Status subresources are controller-owned; users receive read-only status access |

## Plugin Trust Boundary

External plugins are outside the Kapro trust boundary. Kapro sends bounded
requests over the registered protocol and treats plugin responses as advisory
backend results, not as ownership of promotionrun state.

Plugins must not:

- create or mutate `PromotionTarget` objects;
- change `PromotionRun.status`;
- bypass Kapro retries, timeouts, or failure policy;
- store irreplaceable promotionrun state only in plugin memory;
- require cluster-admin credentials for ordinary gate or actuator work.

Plugins should:

- implement TLS and, for production, mTLS;
- run in platform-controlled namespaces;
- use least-privilege service accounts for backend access;
- return deterministic decisions for identical inputs;
- respect context cancellation and configured timeouts;
- emit their own audit logs for backend changes.

## OCI and Signature Trust Model

`PromotionTrigger` is safe by default:

- `spec.suspended` defaults to `true`;
- generated PromotionRuns default to suspended;
- OCI signature verification defaults to required;
- generated PromotionRuns should use immutable digests, not mutable tags;
- cooldown and max-active limits reduce promotionrun floods.

The intended production posture is:

1. CI publishes an OCI artifact and signs it.
2. Kapro observes only tags that match the trigger pattern.
3. Kapro resolves the tag to an immutable digest.
4. Kapro verifies signature policy before promotionrun creation.
5. Kapro creates a suspended, digest-pinned PromotionRun.
6. A promotionrun manager reviews and unsuspends the PromotionRun or trigger according to
   environment policy.

Keyless verification should pin expected issuer and subject identity. Key-based
verification should use a trusted public key distributed through a
platform-owned Secret or ConfigMap. Unsigned artifacts must not create automatic
production PromotionRuns.

### PromotionTrigger with cosign keyless policy

`PromotionTrigger` observes tags and creates a digest-pinned PromotionRun. The promotionplan
gate enforces the cosign keyless identity before target rollout.

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionPlan
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
        gate:
          verification:
            cosignPolicy:
              keyless:
                issuer: https://token.actions.githubusercontent.com
                subject: repo:example/checkout:ref:refs/heads/main
---
apiVersion: kapro.io/v1alpha1
kind: PromotionTrigger
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
  promotionrunTemplate:
    promotionplans:
      - name: production
        promotionplan: checkout-keyless
    suspended: true
    scope:
      targets:
        - checkout-canary-eu
  cooldown: 30m
  maxActive: 1
  dryRun: true
```

### PromotionTrigger with cosign public key policy

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
apiVersion: kapro.io/v1alpha1
kind: PromotionPlan
metadata:
  name: checkout-keyed
spec:
  stages:
    - name: canary
      selector:
        matchLabels:
          kapro.io/tier: canary
      gate:
        mode: auto
        gate:
          verification:
            cosignPolicy:
              key:
                secretRef:
                  name: checkout-cosign-public-key
                  namespace: kapro-system
                  key: cosign.pub
```

## Webhook and Gate Security

Webhook gates call external systems to decide whether a target may advance.
They should be treated as production policy endpoints.

Requirements:

- use HTTPS for all non-development webhook endpoints;
- authenticate requests with mTLS or a shared secret;
- validate request timestamp or nonce when the backend supports it;
- make decisions idempotent for a promotionrun, stage, target, and gate ref;
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
  `PluginRegistration.parameters`;
- never write credential values into status, Events, logs, or notifications.

Rotate registry and plugin credentials independently from promotionrun state. A
credential rotation should not require recreating PromotionRun or PromotionPlan objects.

## Audit Evidence

Kapro records status, Kubernetes Events, and optional lifecycle notifications.
For regulated environments, send lifecycle notifications to an append-only
external sink and retain:

- artifact digest and signature verification result;
- PromotionRun and PromotionTrigger object metadata;
- gate result and message;
- approver identity and bypass flag;
- plugin name, version, and endpoint identity;
- target phase transitions and failure reasons.
