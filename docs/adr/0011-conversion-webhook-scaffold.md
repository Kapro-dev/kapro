# ADR-0011: Conversion Webhook Scaffold

## Status
Accepted

## Context
Kapro made the pre-stable `kapro.io/v1alpha1` to `kapro.io/v1alpha2`
rename as a clean break because there were no supported production users. That
cannot be the permanent upgrade posture. The next served-version transition
will affect early adopters, stored objects, Helm installs, and automation that
expects Kubernetes API reads to keep working across operator upgrades.

## Decision
Kapro publishes an identity conversion webhook handler scaffold for the
`kapro.io/v1alpha2` API line.

When webhooks are enabled, the operator registers `/convert` on the existing
webhook server. The first implementation is intentionally identity-only because
`v1alpha2` is the only served version. It validates that objects are already
supported `kapro.io/v1alpha2` root kinds and returns the raw object unchanged.

Future API versions must replace the identity dispatcher with explicit
per-kind conversion functions, for example
`Convert_v1alpha2_to_v1alpha3_Promotion` and
`Convert_v1alpha3_to_v1alpha2_Promotion`.

The shipped static CRDs do not declare `spec.conversion.strategy: Webhook` yet.
Helm CRDs are not templated, so they cannot safely reference the chart-generated
webhook CA bundle or arbitrary release names/namespaces. Enabling the CRD
conversion strategy requires a follow-up chart design that either injects a
trusted `caBundle`, patches CRDs after cert generation, or moves CRD management
to a templated/install-time path without breaking upgrades.

## Rejected alternatives
- Defer conversion until a second served version exists. That keeps the code
  small now, but leaves the project implementing upgrade plumbing under release
  pressure later.
- Declare `strategy: Webhook` in static Helm CRDs without a CA bundle. That
  looks complete in YAML but fails TLS verification when the API server calls
  the conversion webhook.
- Use controller-runtime's generic conversion handler immediately. It is built
  for multi-version hub/spoke types and rejects same-GVK conversion, while this
  scaffold only has one served version.
- Treat the `v1alpha1` to `v1alpha2` migration as convertible. That would imply
  support for unreleased prototype schemas and old kind names that the project
  deliberately removed before public preview.

## Consequences
- The webhook endpoint and identity conversion tests exist before a second API
  version is introduced.
- CRD manifests remain single-version with no active conversion strategy until
  the chart has a trusted CA injection story.
- The scaffold is not an automatic legacy migration path. Old prototype
  objects still need to be recreated from current examples.
- Public-surface tests now fail if any shipped CRD drops the conversion block
  in without a trusted CA bundle.

## References
- [ADR-0004](0004-camelcase-field-harmonization.md)
- [API stability](../concepts/api-stability.md)
