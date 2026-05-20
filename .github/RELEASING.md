# Releasing Kapro

Kapro releases are pre-stable until a stable Kubernetes API version is
published. Tags still require clean source, current generated assets, explicit
verification, and complete release notes.

## Candidate Checklist

Run from a clean checkout:

```bash
git fetch origin --tags
git switch <release-branch>
git pull --ff-only
go test ./...
make build
make lint
make validate-yaml-json
make check-markdown-links
scripts/verify-install.sh render
```

Run live checks when Docker, Kind, Argo CD, and Flux are available:

```bash
scripts/verify-install.sh kind-demo
scripts/verify-install.sh argo-e2e
scripts/verify-install.sh flux-git-e2e
scripts/verify-install.sh flux-e2e
```

Do not tag if CRDs, generated assets, examples, install render, or release
notes are out of date.

## v0.1.0 Scope

`v0.1.0` is the first public pre-stable release for controlled adopters. It is
not a GA stability promise. Use `CHANGELOG.md` for release notes and
`docs/api-stability.md` for compatibility guidance.

Known limitations to call out:

- all Kubernetes APIs are `kapro.io/v1alpha1`;
- the security model has not had an independent audit;
- production soak across many customer repository styles is limited;
- PromotionTrigger signature policy is the artifact verification path;
- release candidates should publish a real multi-architecture operator image.

## Artifacts

For a tagged release, publish:

- Git tag, for example `v0.1.0`;
- GitHub release notes generated from `CHANGELOG.md`;
- operator image `ghcr.io/kapro-dev/kapro-operator:<tag>`;
- cosign signature for the operator image;
- Helm chart package `kapro-operator-<version>.tgz`;
- checksums for downloadable archives.

## Tagging

After verification and review:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The release workflow builds and pushes the operator image, signs it, packages
the Helm chart, and creates a GitHub release.

## Post-Release Checks

1. Install the chart by tag or packaged artifact into a new Kind cluster.
2. Verify the operator image digest matches the signed image.
3. Confirm README examples reference the released version.
4. Run the Kind demo against the released image.
5. Open follow-up issues for any release-note limitation that blocks adoption.
