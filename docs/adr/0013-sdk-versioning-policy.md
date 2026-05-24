# ADR-0013: Go SDK versioning policy

## Status
Superseded in part by [ADR-0018](0018-public-runtime-api-split.md)

ADR-0018 resets the Kubernetes API packages to `api/kapro/v1alpha1` and
`api/kaproruntime/v1alpha1`. Version references below predate that reset; the
SDK policy remains, but the current SDK targets `kapro.io/v1alpha1`.

## Context

Kapro already exposes Kubernetes API types under `api/v1alpha2` and lifecycle
CloudEvents under `pkg/events`. The new `pkg/kapro` SDK adds a more ergonomic
surface for common user code: builders for user-authored objects, a lightweight
CloudEvents subscriber, and a minimal gate interface.

Once external Go programs import this package, exported names become a preview
compatibility promise even while the Kubernetes APIs are still pre-stable. The
project needs a clear policy so SDK consumers understand what can change between
public preview releases.

## Decision

Ship the Go SDK at `kapro.io/kapro/pkg/kapro`, versioned with the main Kapro Go
module and aligned to the currently served Kubernetes API version.

Kapro remains on pre-stable `0.x.x` release trains until the public API and SDK
contracts graduate. Active GitHub milestones should use exact feature-release
names such as `v0.2.4`, `v0.4.7`, or `v0.4.20`; avoid broad buckets such as
`v0.10.0` and do not treat `1.0.0` as an implementation bucket. The pre-stable
strategy is `0.<capability-line>.<feature-increment>` so both remaining digits
carry product meaning.

For the `v0.1.x` release line:

- `pkg/kapro` targets `kapro.io/v1alpha2`.
- Existing exported names are treated as source-compatible within the release line.
- New builder methods, helper functions, event helpers, and optional fields may
  be added in minor or patch releases.
- Breaking changes require a changelog entry and migration note.
- Full field coverage is not required for the first scaffold; builders cover
  the happy path and return normal API objects that callers may mutate directly.

When Kapro graduates the Kubernetes API to a new served version, the SDK must
either keep compatibility wrappers for the older shape or document the break in
a new release line.

## Rejected alternatives

### Put the SDK under `sdk/`

ADR-0007 originally proposed `sdk/builder`, `sdk/subscriber`, and `sdk/gate`.
That split is useful once the surface grows, but it is heavier than necessary
for public preview. A single `pkg/kapro` package is easier to discover and keeps
the first compatibility promise small.

### Generate builders from CRD schemas

Generated builders would provide broad coverage immediately, but they would also
couple the SDK to kubebuilder generator details and expose every API field
before the user-facing shape has settled. Hand-written builders make the public
surface intentional.

### Treat the SDK as experimental and unstable

That would reduce maintenance burden, but it would undercut the main reason to
ship an SDK: giving CI systems, internal platforms, and integration authors a
contract they can depend on.

## Consequences

The SDK must remain intentionally small. Adding exported names is cheap for
users but permanent for maintainers, so new methods should map to proven
adoption workflows rather than every possible CRD field.

The package stays close to the Kubernetes API. Builders return API objects, not
parallel SDK models, which prevents translation drift and keeps controller
validation authoritative.

## References

- [ADR-0007: Kapro programmatic SDK](0007-programmatic-sdk.md)
- [Go SDK guide](../extending/sdk.md)
