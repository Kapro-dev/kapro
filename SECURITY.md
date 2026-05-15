# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| main    | ✅ |
| < 0.1   | ❌ |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Please report security vulnerabilities by emailing: **vinnxcapital@gmail.com**

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You will receive a response within 72 hours. We follow responsible disclosure — we ask for 90 days to address the issue before public disclosure.

## Security Design Principles

- `kapro-cluster-controller` uses **outbound-only** HTTPS connections to the control plane
- No static credentials — uses Kubernetes ServiceAccount tokens (OIDC/Workload Identity)
- CRDs are the only cross-cluster communication channel
- The control plane never initiates connections to workload clusters

See [docs/security.md](docs/security.md) for the threat model, plugin trust
boundary, OCI signature trust model, webhook/gate security, and Secret handling
guidance.
