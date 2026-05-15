# v0.1.0-alpha Release Runbook

This runbook is the release-readiness checklist for `v0.1.0-alpha`.

## Scope

Release `v0.1.0-alpha` when these surfaces are ready:

- Operator image published as `ghcr.io/kapro-dev/kapro-operator:v0.1.0-alpha`.
- Helm chart renders and installs with `appVersion: v0.1.0-alpha`.
- CRDs in `charts/kapro-operator/crds` match `config/crd/bases`.
- Clean-clone render verification passes.
- At least one disposable cluster install has been verified.
- Local Kind demo has been run or explicitly waived with the reason recorded.

## Pre-Tag Checklist

Run from a clean worktree:

```bash
git status --short
make generate
make manifests
make sync-crds
git diff --exit-code -- api config charts/kapro-operator/crds
scripts/verify-install.sh render
```

Recommended lightweight build checks:

```bash
make build
go test ./cmd/... ./internal/... ./pkg/...
```

Run the full local suite when envtest assets are available:

```bash
make test-no-cover
```

## Install Verification

Follow [Clean-Clone Install Verification](install-verification.md) before
publishing the release announcement.

Minimum acceptance:

```bash
scripts/verify-install.sh render
KAPRO_IMAGE_TAG=v0.1.0-alpha scripts/verify-install.sh cluster
```

If validating before the image is published, load a local image into Kind and
set `KAPRO_IMAGE_REPOSITORY` and `KAPRO_IMAGE_TAG` to that local image.

## Demo Validation

Run the local demo when Docker and Kind are available:

```bash
scripts/verify-install.sh kind-demo
```

Record the final `scripts/kind-demo.sh status` output in the release notes or
release issue. If the demo is waived, record which dependency was unavailable
and keep `scripts/verify-install.sh render` as the minimum validation.

## Tag And Release

Confirm the release workflow is expected to publish:

- Multi-arch operator image for `linux/amd64` and `linux/arm64`.
- Image tag `v0.1.0-alpha`.
- Cosign signature for the image digest.
- Helm chart archive and checksum file attached to the GitHub Release.
- GitHub Release generated from the tag, release notes, and packaged chart.

Create and push the tag:

```bash
git tag -a v0.1.0-alpha -m "v0.1.0-alpha"
git push origin v0.1.0-alpha
```

After the workflow finishes:

```bash
gh release view v0.1.0-alpha
docker buildx imagetools inspect ghcr.io/kapro-dev/kapro-operator:v0.1.0-alpha
cosign verify ghcr.io/kapro-dev/kapro-operator:v0.1.0-alpha \
  --certificate-identity-regexp 'https://github.com/Kapro-dev/kapro/.github/workflows/release.yml@refs/tags/v0.1.0-alpha' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Run one final install check against the published image:

```bash
KAPRO_IMAGE_TAG=v0.1.0-alpha scripts/verify-install.sh cluster
```

## Rollback

If release publication fails before the image is available, delete the failed
GitHub Release and tag, fix the issue, and push a new annotated tag.

If the image is published but install verification fails, leave the tag in
place, mark the GitHub Release as pre-release with a failure note, and publish a
new alpha tag after the fix.
