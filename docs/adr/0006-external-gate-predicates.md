# ADR-0006: External gate predicates — GateType + GateInstance (KEDA-shaped)

## Status
Proposed

## Context

Today every Kapro gate is **inline and built-in**. `Stage.Gate.Gate`
holds a fixed union of capabilities: `soakTime`, `healthCheck`,
`metrics[]`, `templates[]`, and `verification`. The closest extension
point is `GateTemplateSpec.Type: cel | job | webhook | plugin`, but
each row is still authored inline inside the PromotionPlan that uses
it. Reuse across plans is by copy-paste; reuse across organisations
is impossible.

This collides with reality: the union of gates real users actually
need is unbounded. A short, non-exhaustive list of "we want to gate
on X" requests Kapro will never ship code for:

- Cosign attestation chains in *our* internal Rekor
- JIRA / ServiceNow change-request approvals
- Slack `/approve` confirmations from a named on-caller
- Synthetic e2e tests in a per-customer QA cluster
- Monthly regional spend under budget
- "Was this rollout approved by the security team's webhook?"

Two patterns also keep coming up that the inline-union model handles
badly even for built-ins:

- **Reuse across stages.** "All prod stages must pass our cosign
  policy and our SLO check" today means copy-pasting the same gate
  blob into every stage of every plan. PromotionPlan-level
  `metricPresets` exist but only for the metrics axis.
- **Boolean composition.** Some gates want "either A or B passes."
  Some want "A AND (B OR human override)." The implicit-AND-across-
  components model can express none of this.

Both pressures push the same direction: gates should be a **first-
class abstraction with a type system and a deployment model**, not
an inline union baked into the operator. The KEDA project solved
the structurally identical problem for autoscaling: they ship the
FSM, you ship the predicate (Prometheus scaler, AWS SQS scaler,
custom external-scaler-grpc). The Scaler/ScaledObject split is the
analogue we want.

## Decision

Introduce a new cluster-scoped CRD under `kapro.io/v1alpha1` —
`GateType` — and a stage-inline reference to it (`gateTypeRef:`).

### `GateType` — the contract (cluster-scoped, platform-team owned)

```yaml
apiVersion: kapro.io/v1alpha1
kind: GateType
metadata:
  name: internal-cosign
spec:
  protocolVersion: v1                    # ratchet for protocol changes
  description: "Verifies cosign attestations against internal Rekor."
  owners:
    - team: platform-security
  parameters:                            # OpenAPI v3 schema fragment
    type: object
    required: [rekorURL, subject]
    properties:
      rekorURL: { type: string, format: uri }
      subject:  { type: string }
  predicate:
    webhook:
      url: https://gates.internal/cosign
      auth:
        type: oidc                       # oidc | mtls | signedCloudEvent | bearer
        oidc:
          audience: kapro.gates.internal-cosign
          serviceAccount: gate-cosign
      timeout: 30s
      retries:
        max: 3
        backoff: linear
  failureMode: failClosed                # failClosed | failOpen | retryForever
  cache:
    ttl: 60s                             # dedup identical evaluations
```

`GateType` is a **template**. It has **no controller and no status**
— the same model as `PromotionPlan` and `BackendProfile`. The
admission webhook validates the parameter schema is well-formed
OpenAPI and that the predicate block names a reachable transport.

### Stage-inline reference

```yaml
gate:
  mode: auto
  gate:
    gateTypeRef: internal-cosign
    parameters:
      rekorURL: https://rekor.internal
      subject: spiffe://corp/team/checkout
```

The `parameters:` map is validated against
`GateType.spec.parameters` at admission time.

### Protocol (`v1`)

Operator → predicate webhook:

```json
{
  "specversion": "1.0",
  "type": "kapro.io/promotion.gate.evaluate.v1",
  "id": "<idempotency-uuid>",
  "source": "kapro-operator/<cluster-id>",
  "time": "2026-05-20T10:00:00Z",
  "data": {
    "gateType": "internal-cosign",
    "parameters": { "rekorURL": "...", "subject": "..." },
    "context": {
      "promotion": "checkout-v1.2.3",
      "promotionRun": "checkout-v1.2.3-001",
      "stage": "prod",
      "target": "eu-prod-1",
      "version": "v1.2.3",
      "previousVersion": "v1.2.2"
    },
    "attempt": 1
  }
}
```

Predicate → operator response (HTTP 200 with a CloudEvent body):

```json
{
  "specversion": "1.0",
  "type": "kapro.io/promotion.gate.result.v1",
  "id": "<echoed-request-id>",
  "source": "internal-cosign/<predicate-instance>",
  "time": "2026-05-20T10:00:01Z",
  "data": {
    "passed": true,
    "reason": "rekor:sha256:abc123 verified for spiffe://corp/team/checkout",
    "retryAfter": null,
    "advisories": []
  }
}
```

Non-200, 5xx, or context-deadline violations are governed by
`GateType.spec.failureMode`. `protocolVersion` lets us ship `v2`
without breaking v1 predicates.

## Rejected alternatives

### A. Full boolean-composition DSL inside `Stage.Gate`
The first instinct: add `anyOf`/`allOf`/`not` operators. The
mechanical evaluation is 20 lines; the observability, error
messages, and `kapro diag` UX are months of work. ArgoCD's CEL-
based RBAC and OPA Gatekeeper rules became unreadable for everyone
except their authors. `GateType`-as-role-selection covers the bulk
of real composition use cases without opening the De Morgan footgun.

### B. `GateInstance` as a separate CRD
The full Service/EndpointSlice analogy would put `GateType`
(template) and `GateInstance` (per-stage binding) in separate CRDs.
We rejected it for the v1 surface: every Stage already declares its
own gate block, and Promotion already plays the Service-ish role.
Adding a third CRD per stage doubles the user's mental model with
no payoff. If a future need emerges — e.g. sharing a `GateInstance`
across stages with shared parameter values — we can promote it then.
Inline-ref-only is the forward-compatible subset.

### C. gRPC, like KEDA's external scaler
KEDA chose gRPC because it pre-dated CloudEvents standardisation
and needed bidirectional streaming. Our predicates are request /
response. We already own a polished HTTP+CloudEvents stack from
ADR-0003, PR #79, PR #80, and PR #91. Picking gRPC means a whole
new transport surface for no capability gain.

### D. In-process plugin hooks
Register a Go function with the operator at startup; the operator
calls it inline during reconcile. Every CD product that has shipped
this (Drone, Concourse, ArgoCD's early Lua hooks, Tekton's
WhenExpressions plugin path) has regretted it. The operational tax
of "redeploy the operator whenever the predicate changes" is
permanent, and a misbehaving plugin sits inside the reconcile loop.
Out-of-process via webhook is where production users converge.

### E. CEL-only escape hatch
CEL gates exist today (`GateTemplateSpec.Type: cel`). CEL can only
reason over data the operator already has; it cannot call out to a
JIRA API or a regional spend service. CEL stays valid as one
implementation *inside* a future `GateType.spec.predicate.cel:`
block — not as a substitute for the abstraction.

## Consequences

**Easier:**
- Platform teams ship a `GateType` once; rollout authors reference
  it by name. Reuse becomes one YAML field.
- The unbounded set of "we want to gate on X" requests stops
  pressuring the upstream CRD shape.
- The protocol is a CloudEvents request/response — same stack as
  existing per-Promotion lifecycle webhooks (ADR-0003). One set of
  metrics, one set of failure-mode primitives.
- `kapro diag` can show "Gate X (type=internal-cosign, params=...,
  duration=12s, last=fail-closed waiting)" — concrete, type-aware
  observability.

**Harder:**
- One new CRD to learn (`GateType`). The admission webhook gets a
  new validation path (OpenAPI parameter schema).
- The operator now talks to user-controlled HTTP endpoints during
  gate evaluation. SSRF, latency-injection, and credential-exposure
  threat models apply — mitigated by the same `KAPRO_LIFECYCLE_*`
  controls we already operate.
- Predicate authors must implement an HTTP server. Mitigated by
  `sdk/gate` (ADR-0007) shipping the scaffold.

**Locks in:**
- `GateType.spec.protocolVersion` as the ratchet. v1 is what this
  ADR defines. v2 ships under a new field; the operator routes by
  version.
- `failureMode` defaults to `failClosed` — safer default for CD.
- `cache.ttl` is opt-in per type. Aggressive caching is the user's
  choice.

**Risks to mitigate before ship:**
1. Cache invalidation. Identical-parameter evaluations within
   `cache.ttl` reuse the result. Time-sensitive gates (budget)
   require short or zero TTL. Doc this loudly.
2. Predicate timeouts that exceed the Stage's gate timeout. The
   operator owns the overall budget; per-attempt sub-budgets derive
   from remaining time. Same model as PR #80 sink work.
3. Predicates that always return `passed: true`. Not our problem to
   enforce, but `kapro diag` must surface advisory count and reason
   prominently so operators notice a never-blocking gate.

## References

- ADR-0001 — Service/EndpointSlice split (we apply the same pattern
  to gates).
- ADR-0003 — CloudEvents publisher posture (the protocol we reuse).
- PR #79 — `internal/lifecycle.Dispatcher` (the transport we extend).
- PR #80 — Sink retries, timeout-with-overall-context, SSRF guard.
- PR #91 — `examples/plugins/cloudevents-printer` (the reference
  pattern `sdk/gate` will productise).
- KEDA Scaler / ScaledObject split — the structural analogue.
- ADR-0007 — The Kapro SDK that ships `sdk/gate` for predicate authors.
