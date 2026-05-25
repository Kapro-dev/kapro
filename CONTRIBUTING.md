# Contributing to Kapro

Thank you for your interest in contributing to Kapro.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md).

## Development Requirements

- Go 1.25+
- [controller-gen](https://book.kubebuilder.io/reference/controller-gen.html) v0.21.0
- [golangci-lint](https://golangci-lint.run/) v2.12.2
- kubectl + a running Kubernetes cluster (for e2e)

## Getting Started

```bash
git clone https://github.com/Kapro-dev/kapro
cd kapro
go mod tidy
make generate    # generate CRD manifests + DeepCopy methods
make build       # compile binaries
make test        # run unit tests with envtest
scripts/verify-install.sh render
```

## Repository Layout

- `docs/` contains user-facing concepts, operations, and provider setup docs.
- `examples/` contains runnable examples and optional provider-specific helpers.
- `scripts/` contains repository development, CI, and verification scripts.
- `build/` contains build-time metadata used by generators and release tooling.
- Provider-specific onboarding helpers should live under `examples/04-substrates/02-cloud/`; core
  Kapro APIs and controllers should stay cloud-neutral unless a provider
  integration requires dedicated code.

## Pull Request Process

1. Fork the repo and create a feature branch from `main`
2. Make your changes following the [Kubernetes API conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)
3. Run `make lint test` before opening a PR
4. Each PR must include:
   - A clear description of the change
   - Test coverage for new code
   - Updated docs if behaviour changes
5. DCO sign-off is required: `git commit -s`. This adds a
   `Signed-off-by:` line certifying the
   [Developer Certificate of Origin](https://developercertificate.org/).

## Coding Standards

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use structured logging: `log.FromContext(ctx).Info(...)`
- Controller reconcilers must be idempotent
- Status conditions follow [Kubernetes condition conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties)
- All exported types, functions, and constants must have GoDoc comments

## CRD Changes

- All CRD changes require a `+kubebuilder:` marker update
- Run `make manifests` to regenerate CRD YAMLs after type changes
- Breaking API changes are not allowed in the public preview API without a new API version
  or an explicit migration plan. See [API Stability](docs/api-stability.md).

## Reporting Issues

Use GitHub Issues. For security issues, see [SECURITY.md](SECURITY.md).
