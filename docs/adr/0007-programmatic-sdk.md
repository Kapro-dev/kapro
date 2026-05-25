# ADR-0007: Kapro programmatic SDK — builder + subscriber + gate

## Status
Proposed

## Context

Today Kapro's public Go surface is two packages:

- `kapro.io/kapro/api/v1alpha1` — the CRD Go types (importable).
- `kapro.io/kapro/pkg/events` — the CloudEvents vocabulary (stable).

A user who wants to integrate programmatically — for example a CI
pipeline that creates Promotions, a notification bridge that listens
to Promotion events, or a custom gate predicate — currently does
one of three things:

1. Hand-rolls verbose Go struct literals against `api/v1alpha1`.
2. Templates YAML and calls `kubectl apply`.
3. Re-implements the CloudEvents subscriber pattern from
   `examples/05-plugins/05-cloudevents-printer` (PR #91) inside their app.

None of these scale. The struct-literal path is correct but
ergonomically harsh. YAML templating loses type safety. Re-inventing
the subscriber wastes the ~150 lines we already shipped as a
reference.

Three concrete user requests have surfaced:

- **"Construct Promotions / PromotionPlans programmatically."** CI
  systems and platform teams want typed Go (and eventually Python)
  builders, not YAML.
- **"Hook into the state machine."** Users want to react to phase
  transitions — `OnPhase(Succeeded, slackNotify)` — without
  redeploying the operator. This is the framing the user described
  as an "activation function" — borrowed from KEDA's external-
  scaler shape (ADR-0006).
- **"Write a custom gate predicate."** Once ADR-0006 lands, the
  predicate protocol exists but predicate authors should not have
  to implement CloudEvents decoding, auth, retry-safe ack
  semantics, or panic recovery from scratch.

Workflow-as-code frameworks (Airflow, Dagster, Temporal, Argo
Workflows) all converged on similar layered SDKs. Their lesson is
that the SDK becomes a versioned contract the moment it ships — so
the surface must be minimal and obviously aligned with the engine's
abstractions, or it accretes API debt forever.

## Decision

Ship the Kapro SDK as **three small packages** under
`kapro.io/kapro/sdk/`. Each package is independently useful, and
the three together exhaust the integration patterns we have agreed
to support.

### `sdk/builder` — typed manifest construction

Fluent constructors that produce `api/v1alpha1` objects, ready for
`controller-runtime/client.Create()` or YAML serialisation.

```go
import (
    "time"

    kapro "kapro.io/kapro/sdk/builder"
)

plan := kapro.NewPromotionPlan("checkout-progressive").
    Stage("canary").Selector("kapro.io/tier", "canary").
        Gate(kapro.AutoGate().Soak(10 * time.Minute).HealthCheck()).
    Stage("prod").Selector("kapro.io/tier", "production").
        After("canary").RequiredSoak(10 * time.Minute).
    Build()

promo := kapro.NewPromotion("checkout-v1.2.3").
    KaproRef("checkout").
    Version("v1.2.3").
    Plan(plan).
    Timeout(30 * time.Minute).
    Lifecycle(
        kapro.OnSucceeded(kapro.Webhook("https://hooks.slack.../approved")),
    ).
    Build()

_ = client.Create(ctx, promo)
```

Constraints:
- **No new public types.** The builder produces `*v1alpha1.Promotion`,
  `*v1alpha1.PromotionPlan`, etc. — same objects users already
  consume.
- **No runtime.** Pure synchronous library, no goroutines, no
  network. Easy to fuzz, easy to test.
- **No code generation.** Hand-written, because the surface is
  small (~200 LoC) and code-gen would tie us to a kubebuilder
  major version forever.

### `sdk/subscriber` — server scaffold for CloudEvents callbacks

Productises the `cloudevents-printer` pattern from PR #91. Handles
CloudEvents v1.0 envelope decoding, `X-Kapro-Auth` validation
(constant-time compare, same as the reference subscriber), retry-
safe ack semantics, panic recovery, structured logging,
Prometheus metrics. User writes only the handler bodies.

```go
import (
    "context"

    "kapro.io/kapro/pkg/events"
    "kapro.io/kapro/sdk/subscriber"
)

// KAPRO_PRINTER_AUTH_HEADER is the env-var name the reference
// subscriber (examples/05-plugins/05-cloudevents-printer) uses. The SDK
// is unopinionated — pick whichever name fits your binary.
sub := subscriber.New(":8080",
    subscriber.WithAuthHeader(os.Getenv("KAPRO_SUBSCRIBER_AUTH_HEADER")),
    subscriber.WithMaxBody(1<<20),
)

sub.OnPhase(events.PromotionPhaseSucceeded, func(ctx context.Context, env events.Envelope) error {
    return slack.Notify(env.Data.Promotion, env.Data.Version)
})

sub.OnEvent(events.EventStageGateFailed, func(ctx context.Context, env events.Envelope) error {
    return pagerduty.Page(env.Data.Stage, env.Data.Reason)
})

if err := sub.ListenAndServe(context.Background()); err != nil {
    log.Fatal(err)
}
```

Constraints:
- **One callback per (phase | event type).** No fan-out, no
  prioritisation, no middleware chain. Composition is the user's
  job; the SDK is unopinionated.
- **Handlers receive an `events.Envelope`.** Same type as
  `pkg/events`. The SDK does not invent a parallel "rich" type.
- **Returns ack 204 on success, 5xx on handler error.** The
  operator-side sink retries on 5xx with its existing budget
  (PR #80). Idempotency is the user's responsibility — same
  contract as Argo Notifications.

### `sdk/gate` — server scaffold for external gate predicates

Depends on ADR-0006 (`GateType` v1 protocol). Productises the
predicate side of the gate evaluation flow.

```go
import (
    "context"

    "kapro.io/kapro/sdk/gate"
)

srv := gate.NewServer(":9090",
    gate.WithAuth(gate.OIDCAuth(
        gate.OIDCAudience("kapro.gates.internal-cosign"),
    )),
)

srv.Register("internal-cosign", func(ctx context.Context, p gate.Params) gate.Result {
    // p.Parameters["rekorURL"] / p.Parameters["subject"] etc.
    ok, err := cosign.Verify(p.Parameters["rekorURL"], p.Parameters["subject"])
    if err != nil {
        return gate.Result{Passed: false, Reason: "rekor lookup failed: " + err.Error()}
    }
    return gate.Result{Passed: ok, Reason: "rekor verified"}
})

srv.Register("budget-check", budgetPredicate)

_ = srv.ListenAndServe(context.Background())
```

Constraints:
- **One Go function per `GateType` name.** No reflection, no
  registry of plugins-of-plugins.
- **Caching is the operator's job, not the predicate's.** The
  `GateType.spec.cache.ttl` field (ADR-0006) controls dedup. The
  predicate is free to be expensive.
- **Auth helpers cover the four ADR-0006 auth modes.** mTLS,
  OIDC, bearer, signed-CloudEvent. The SDK ships defaults; the
  user picks one.

## Rejected alternatives

### A. Single `sdk/` package with everything
Tempting for the homepage example, but it makes every transitive
import drag in the others. A CI tool that only wants to build
manifests should not need the HTTP server transitive deps from
`sdk/subscriber`. Three packages = three import graphs, three test
suites, three independent stability surfaces.

### B. In-process plugin hooks
Register a Go function with the operator at startup. Same rejection
as ADR-0006(D): every CD product that has shipped this has regretted
it. The operational tax of "redeploy the operator whenever the
callback changes" is permanent. Out-of-process via `sdk/subscriber`
is the supported path.

### C. Python / TypeScript SDKs in this ADR
Python is a real ask, especially from CI authors. Two reasons to
defer:
- The Go SDK has not stabilised. Shipping a Python binding before
  Go v1 means we publish *two* contracts the moment Go changes.
- Code-generated Kubernetes Python clients drift fast. Maintaining
  one is its own product. We will not staff that until Go is
  battle-tested by at least three real users.

A future ADR (likely 00NN) will scope Python. Out for now.

### D. Workflow-as-code (multi-Promotion DAG composer)
A `kapro.Fleet().Promote(...).After(...)` API that composes many
Promotions across many regions / days into a higher-order rollout.
Compelling — and almost certainly the seed of a future product. But
it is a **layer above** what Kapro models today. Argo Workflows and
Crossplane Compositions are in this space. Drift into it and we
spend 18 months on workflow-engine concerns (replay, idempotency,
deterministic execution) instead of CD concerns. If real users want
this composition, they can write 50 lines on top of `sdk/builder` —
which is the right boundary.

### E. Code-generated builder from CRD OpenAPI
The OpenAPI → builder generator path (Crossplane Provider Runtime,
controller-tools) exists. Rejected for `sdk/builder` v1: the
surface is small enough to hand-write, and the generator constrains
the ergonomic shape to what OpenAPI can express. Hand-rolled lets
us pick names like `CanaryThenFull(...)` and `AutoGate().Soak(...)`
that match how operators *think*, not how the schema *renders*.

## Consequences

**Easier:**
- CI authors and platform teams build manifests in Go without YAML
  templating drudgery.
- The CloudEvents subscriber pattern stops being copy-paste from
  `examples/`. The ~150 LoC of decoding / auth / retry / metrics
  ship once and everyone consumes them.
- Gate predicate authors (ADR-0006) get a 10-line `Register(...)`
  surface instead of a custom HTTP server.
- Each package versions independently against its own contract:
  `builder` versions with `api/v1alpha1`; `subscriber` versions
  with `pkg/events`; `gate` versions with the ADR-0006 protocol.

**Harder:**
- Three packages = three public APIs we are now responsible for.
  Stability bar matches `pkg/events` (ADR-0003): breaking changes
  require a new major version or a clear deprecation cycle.
- A `sdk/subscriber` user who upgrades behind the operator may see
  new event types in the data field they did not request. We
  inherit the ADR-0003 contract: new event types are additive, the
  envelope shape is stable.

**Locks in:**
- The three-package split. Future "I want to also do X" requests
  must fit one of `builder` / `subscriber` / `gate` or motivate a
  fourth ADR.
- Hand-rolled builder. We will not retrofit code-gen without a
  superseding ADR.
- Go-first. Python ships only after Go stabilises, in a separate
  ADR.

**Risks to mitigate before ship:**
1. **`sdk/builder` becoming a second source of truth.** If the
   builder lets you express something the CRD does not validate,
   you have a phantom API. Lint rule: every builder method must
   correspond to a single `api/v1alpha1` field. A drift canary
   test (analogous to the `pkg/events` doc canary) enforces it.
2. **`sdk/subscriber` and `sdk/gate` overlap.** Both run an HTTP
   server. Resist sharing internals between them — they have
   different protocols and lifecycles. Coincidental code reuse via
   a third `sdk/transport` helper is OK only if both surfaces ask
   for it concretely.
3. **Versioning UX.** Go modules version per-package by import
   path, but `go.mod` versions the whole module. We will bump
   `kapro.io/kapro` at the speed of its fastest-moving SDK package.
   Document this in `docs/api-stability.md` before any `sdk/*` v1.

## References

- ADR-0001 — Promotion / PromotionRun split (the model
  `sdk/builder` makes concrete in Go).
- ADR-0003 — CloudEvents publisher posture (`sdk/subscriber`
  consumes the same envelope shape).
- ADR-0006 — External gate predicates (`sdk/gate` ships the
  predicate side).
- `pkg/events` — already-public CloudEvents vocabulary.
- `api/v1alpha1` — already-public CRD types.
- PR #91 — `examples/05-plugins/05-cloudevents-printer` (the reference
  subscriber `sdk/subscriber` productises).
