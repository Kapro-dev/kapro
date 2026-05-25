# Security Policy

Kapro is a Kubernetes promotion control plane. Treat access to Kapro CRDs,
plugin registration, promotionrun triggers, approvals, and referenced Secrets as
production-change authority.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.6.x   | ✅ |
| main    | Best effort before the next tag |
| < 0.6.0 | ❌ |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Please report security vulnerabilities with GitHub private vulnerability
reporting:

<https://github.com/Kapro-dev/kapro/security/advisories/new>

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You will receive a response within 72 hours. We follow responsible disclosure
and ask for 90 days to address the issue before public disclosure.

## Security Design Principles

- Pull-mode spokes use `kapro-cluster-controller` with outbound-only HTTPS
  connections to the control plane.
- No static credentials — uses Kubernetes ServiceAccount tokens (OIDC/Workload Identity)
- CRDs are the only cross-cluster communication channel
- The control plane never initiates connections to workload clusters
- Autonomous promotionrun creation is suspended by default and should require OCI
  digest pinning plus signature verification
- External plugins are outside the core trust boundary and must be registered
  only by platform administrators
- Secrets are referenced by name and namespace; credential values must not be
  embedded in CRD specs, status, Events, logs, or notifications

## Security Architecture

Kapro's RBAC, multi-tenancy, plugin trust boundary, OCI signature model,
webhook security, Secret handling, and threat model are documented in
[docs/operations/security.md](docs/operations/security.md) and
[docs/operations/rbac-tenancy.md](docs/operations/rbac-tenancy.md). Plugin
trust details are in [docs/extending/plugin-authoring.md](docs/extending/plugin-authoring.md),
and autonomous PromotionRun creation policy is in
[docs/adr/0001-promotion-runtime-split.md](docs/adr/0001-promotion-runtime-split.md),
which restricts PromotionRun writes to the controller's service account.
