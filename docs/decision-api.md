# Decision API

The Decision API is an optional HTTP surface for reading promotion context and
submitting audited gate decisions. It is disabled by default and should only be
enabled behind Kubernetes authentication and RBAC.

Use it when an external system needs to inspect a PromotionRun, evaluate a
target gate, or submit an approval-like decision without writing directly to
controller-owned status.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/fleet` | Bounded fleet summary for selected Kapro resources. |
| `GET` | `/api/v1/promotionruns/{name}/context` | PromotionRun context: plan, targets, phase, and gate state. |
| `GET` | `/api/v1/promotionruns/{name}/targets/{key}/gate` | Per-target gate evidence for a decision client. |
| `POST` | `/api/v1/promotionruns/{name}/targets/{key}/decide` | Submit approve, reject, or defer with reasoning. |
| `POST` | `/api/v1/promotionruns/{name}/targets/{key}/override` | Record a human override. |

List endpoints accept `limit`, `labelSelector`, and `phase` query parameters.
Responses include pagination metadata and `page.truncated=true` when the server
returned a bounded subset.

## Authorization

Every request uses a Kubernetes bearer token. The operator validates the token
with `TokenReview` and checks the requested action with `SubjectAccessReview`
before reading context or writing a decision.

`AgentPolicy` can further constrain an external decision client by:

- allowed PromotionRun or target selectors;
- allowed decision types;
- confidence thresholds;
- cooldowns and rate limits;
- recommend-only versus autonomous modes.

Decision submissions are recorded on `PromotionTarget.status.decisionTrace` and
also result in normal approval objects when they unblock a manual gate.

## Enabling

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set decisionAPI.enabled=true
```

See [Install](install.md), [Security](security.md), and
[RBAC and Tenancy](rbac-tenancy.md) before exposing this API outside a local
operator network.
