# Contributing to Kapro

Thank you for your interest in contributing! Kapro follows [CNCF Community](https://github.com/cncf/foundation/blob/main/code-of-conduct.md) standards.

## Code of Conduct

This project follows the [CNCF Code of Conduct](CODE_OF_CONDUCT.md).

## Development Requirements

- Go 1.22+
- Node.js 20+ (for UI)
- [controller-gen](https://book.kubebuilder.io/reference/controller-gen.html) v0.14+
- [golangci-lint](https://golangci-lint.run/) v1.57+
- kubectl + a running Kubernetes cluster (for e2e)

## Getting Started

```bash
git clone https://github.com/vinnxcapital-gif/kapro
cd kapro
go mod tidy
make generate    # generate CRD manifests + DeepCopy methods
make build       # compile binaries
make test        # run unit tests with envtest
```

## Pull Request Process

1. Fork the repo and create a feature branch from `main`
2. Make your changes following the [Kubernetes API conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)
3. Run `make lint test` before opening a PR
4. Each PR must include:
   - A clear description of the change
   - Test coverage for new code
   - Updated docs if behaviour changes
5. DCO sign-off is required: `git commit -s`

## Coding Standards

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use structured logging: `log.FromContext(ctx).Info(...)`
- Controller reconcilers must be idempotent
- Status conditions follow [Kubernetes condition conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties)
- All exported types, functions, and constants must have GoDoc comments

## CRD Changes

- All CRD changes require a `+kubebuilder:` marker update
- Run `make manifests` to regenerate CRD YAMLs after type changes
- Breaking API changes are not allowed in `v1alpha1` without a new API version

## Reporting Issues

Use GitHub Issues. For security issues, see [SECURITY.md](SECURITY.md).
