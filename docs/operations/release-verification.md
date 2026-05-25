# Release Verification

Kapro release artifacts are intended to be consumed by immutable version, not
by mutable image tags. Pin charts and images to the same `v0.x.y` release tag
and verify the artifacts before installing them in production change windows.

The current public preview release is `v0.6.0`.

Use [Public Preview 0.6 Checklist](public-preview-0.6-checklist.md) before
cutting the tag; this page verifies already-published artifacts.

## Prerequisites

Install these tools on the verification host:

- `curl`
- `shasum`
- `cosign`
- GitHub CLI `gh`
- `helm`, `kubectl`, and `kind` when running the install smoke

Authenticate `gh` with permission to read public attestations:

```bash
gh auth status
```

## Download Release Assets

```bash
KAPRO_VERSION=v0.6.0
KAPRO_REPO=Kapro-dev/kapro

mkdir -p "kapro-${KAPRO_VERSION}"
cd "kapro-${KAPRO_VERSION}"

for asset in \
  "kapro-operator-${KAPRO_VERSION#v}.tgz" \
  "kapro-operator-${KAPRO_VERSION#v}.tgz.sig" \
  "kapro-operator-${KAPRO_VERSION#v}.tgz.pem" \
  "kapro-cluster-controller-${KAPRO_VERSION#v}.tgz" \
  "kapro-cluster-controller-${KAPRO_VERSION#v}.tgz.sig" \
  "kapro-cluster-controller-${KAPRO_VERSION#v}.tgz.pem" \
  "kapro-operator.spdx.json" \
  "kapro-cluster-controller.spdx.json" \
  "checksums.txt"; do
  curl -fsSLO "https://github.com/${KAPRO_REPO}/releases/download/${KAPRO_VERSION}/${asset}"
done
```

## Verify Checksums

```bash
shasum -a 256 -c checksums.txt
```

Do not continue if any checksum fails.

## Verify Helm Chart Signatures

The Helm chart packages are signed with Sigstore keyless signing. Verify the
signature and certificate identity for each chart package:

```bash
cosign verify-blob \
  --signature "kapro-operator-${KAPRO_VERSION#v}.tgz.sig" \
  --certificate "kapro-operator-${KAPRO_VERSION#v}.tgz.pem" \
  --certificate-identity-regexp="^https://github.com/${KAPRO_REPO}" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  "kapro-operator-${KAPRO_VERSION#v}.tgz"

cosign verify-blob \
  --signature "kapro-cluster-controller-${KAPRO_VERSION#v}.tgz.sig" \
  --certificate "kapro-cluster-controller-${KAPRO_VERSION#v}.tgz.pem" \
  --certificate-identity-regexp="^https://github.com/${KAPRO_REPO}" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  "kapro-cluster-controller-${KAPRO_VERSION#v}.tgz"
```

## Verify Image Signatures

Kapro release images are published with immutable version tags:

- `ghcr.io/kapro-dev/kapro-operator:${KAPRO_VERSION}`
- `ghcr.io/kapro-dev/kapro-cluster-controller:${KAPRO_VERSION}`

Do not install `:latest` in production. Release automation does not treat it as
a compatibility contract.

```bash
for image in kapro-operator kapro-cluster-controller; do
  cosign verify "ghcr.io/kapro-dev/${image}:${KAPRO_VERSION}" \
    --certificate-identity-regexp="^https://github.com/${KAPRO_REPO}" \
    --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
done
```

## Verify SBOM Attestations

```bash
for image in kapro-operator kapro-cluster-controller; do
  cosign verify-attestation --type spdxjson \
    "ghcr.io/kapro-dev/${image}:${KAPRO_VERSION}" \
    --certificate-identity-regexp="^https://github.com/${KAPRO_REPO}" \
    --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
done
```

The release also attaches `kapro-operator.spdx.json` and
`kapro-cluster-controller.spdx.json` as downloadable evidence for offline
review.

## Verify Build Provenance

GitHub artifact attestations are attached to the container images and chart
artifacts:

```bash
gh attestation verify "oci://ghcr.io/kapro-dev/kapro-operator:${KAPRO_VERSION}" \
  --repo "${KAPRO_REPO}"

gh attestation verify "oci://ghcr.io/kapro-dev/kapro-cluster-controller:${KAPRO_VERSION}" \
  --repo "${KAPRO_REPO}"

gh attestation verify "kapro-operator-${KAPRO_VERSION#v}.tgz" \
  --repo "${KAPRO_REPO}"
```

## Smoke the Published Release

After cryptographic verification, install the published chart into a disposable
cluster:

```bash
git clone --branch "${KAPRO_VERSION}" "https://github.com/${KAPRO_REPO}.git" kapro
cd kapro

kind create cluster --name kapro-release-verify --image kindest/node:v1.30.0
kubectl config use-context kind-kapro-release-verify

KAPRO_RELEASE_VERSION="${KAPRO_VERSION}" \
KAPRO_VERIFY_CLEANUP=true \
scripts/verify-install.sh release-cluster

kind delete cluster --name kapro-release-verify
```

For upgrade evidence on a supported upgrade path, install the previous release
and upgrade to the current release. For `v0.6.0`, use the current tag as the
previous version to verify Helm upgrade mechanics; pre-0.6 prototype CRDs require
the [pre-0.6 API reset](../migration/pre-0.6-api-reset.md) cleanup/migration
instead of an in-place CRD upgrade.

```bash
KAPRO_RELEASE_VERSION="${KAPRO_VERSION}" \
KAPRO_PREVIOUS_RELEASE_VERSION="${KAPRO_PREVIOUS_VERSION:-${KAPRO_VERSION}}" \
KAPRO_VERIFY_CLEANUP=true \
scripts/verify-install.sh release-upgrade-cluster
```

For rollback evidence:

```bash
KAPRO_RELEASE_VERSION="${KAPRO_VERSION}" \
KAPRO_PREVIOUS_RELEASE_VERSION="${KAPRO_PREVIOUS_VERSION:-${KAPRO_VERSION}}" \
KAPRO_VERIFY_CLEANUP=true \
scripts/verify-install.sh release-rollback-cluster
```
