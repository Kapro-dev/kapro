#!/usr/bin/env bash
# cut-release.sh — orchestrate a Kapro tag → release workflow run → published
# artifact verification. Pure mechanics; the human decides whether the
# release is good *before* invoking this script.
#
# Usage:
#   VERSION=v0.1.2 scripts/cut-release.sh
#   VERSION=v0.1.2 KAPRO_RELEASE_SKIP_TAG=true scripts/cut-release.sh   # re-watch existing tag
#
# Required env:
#   VERSION                Tag to push (must start with 'v'). Required.
#
# Optional env:
#   KAPRO_RELEASE_SKIP_TAG     If 'true', skip git tag + push (the tag is
#                              assumed to already exist on origin). Useful
#                              for re-running the watch step after a
#                              workflow retry.
#   KAPRO_RELEASE_SKIP_WATCH   If 'true', skip `gh run watch` (just push
#                              the tag and exit).
#   KAPRO_RELEASE_DRY_RUN      If 'true', print what would happen and exit.
#   KAPRO_RELEASE_REMOTE       Git remote (default 'origin').
#
# What this does:
#   1. Sanity-checks VERSION format, working tree cleanliness, current
#      branch is main (or KAPRO_RELEASE_ALLOW_BRANCH=true).
#   2. Confirms HEAD passes the release-smoke target locally.
#   3. Creates an annotated tag (signed if user.signingkey is set) and
#      pushes it to the remote.
#   4. Watches the release workflow run; aborts with a non-zero exit code
#      if the workflow fails.
#   5. Prints links to the GitHub Release, published image digests, and
#      the verification command set for cosign / SBOM / SLSA / chart.
#
# What this does NOT do:
#   - Doesn't run release-cluster smoke against the published chart. Run
#     `scripts/verify-install.sh release-cluster` separately against a
#     fresh kind cluster.
#   - Doesn't update CHANGELOG.md, README.md, install.md. Those should be
#     in `main` before this script is invoked.

set -euo pipefail

VERSION="${VERSION:-}"
REMOTE="${KAPRO_RELEASE_REMOTE:-origin}"
SKIP_TAG="${KAPRO_RELEASE_SKIP_TAG:-false}"
SKIP_WATCH="${KAPRO_RELEASE_SKIP_WATCH:-false}"
DRY_RUN="${KAPRO_RELEASE_DRY_RUN:-false}"
ALLOW_BRANCH="${KAPRO_RELEASE_ALLOW_BRANCH:-false}"

# OWNER/REPO derived from the upstream remote so a fork doesn't accidentally
# target the upstream release.
GH_NWO="$(git remote get-url "${REMOTE}" \
  | sed -E 's#(.*github\.com[:/])([^/]+/[^/]+)\.git#\2#; s#(.*github\.com[:/])([^/]+/[^/]+)#\2#')"
if [[ -z "${GH_NWO}" || ! "${GH_NWO}" =~ ^[^/]+/[^/]+$ ]]; then
  echo "abort: could not parse GitHub owner/repo from remote ${REMOTE}; got ${GH_NWO:-<empty>}" >&2
  echo "set KAPRO_RELEASE_REMOTE to a GitHub remote such as git@github.com:owner/repo.git or https://github.com/owner/repo.git" >&2
  exit 1
fi

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

abort() {
  echo "abort: $*" >&2
  exit 1
}

dry() {
  if [ "${DRY_RUN}" = "true" ]; then
    echo "(dry-run) $*"
    return 0
  fi
  return 1
}

# ---- preflight --------------------------------------------------------------

need git
need gh
[ "${DRY_RUN}" = "true" ] || need make

[ -n "${VERSION}" ] || abort "VERSION not set; usage: VERSION=v0.1.2 scripts/cut-release.sh"
[[ "${VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] \
  || abort "VERSION ${VERSION} is not a valid semver tag (expected vMAJOR.MINOR.PATCH[-PRE])"

branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "${branch}" != "main" ] && [ "${ALLOW_BRANCH}" != "true" ]; then
  abort "current branch ${branch} is not main; set KAPRO_RELEASE_ALLOW_BRANCH=true to override"
fi

if [ -n "$(git status --porcelain)" ]; then
  abort "working tree is dirty, including untracked files; commit, stash, or remove changes before cutting release"
fi

if [ "${SKIP_TAG}" != "true" ] && git rev-parse -q --verify "refs/tags/${VERSION}" >/dev/null; then
  abort "tag ${VERSION} already exists locally; pass KAPRO_RELEASE_SKIP_TAG=true to re-watch the workflow"
fi

echo "cutting release ${VERSION} on ${GH_NWO} (branch=${branch}, remote=${REMOTE})"

# ---- release-smoke gate -----------------------------------------------------

if [ "${DRY_RUN}" != "true" ]; then
  echo "running release-smoke target before tagging"
  if ! make release-smoke >/tmp/kapro-release-smoke.log 2>&1; then
    echo "release-smoke target failed; tail:" >&2
    tail -40 /tmp/kapro-release-smoke.log >&2 || true
    abort "release-smoke must pass before pushing the tag"
  fi
  echo "release-smoke ok"
fi

# ---- tag + push -------------------------------------------------------------

if [ "${SKIP_TAG}" != "true" ]; then
  tag_args=(-a "${VERSION}" -m "${VERSION}")
  if git config --get user.signingkey >/dev/null 2>&1; then
    tag_args=(-s "${VERSION}" -m "${VERSION}")
    echo "creating signed tag ${VERSION}"
  else
    echo "creating annotated (unsigned) tag ${VERSION}"
  fi

  if dry "git tag ${tag_args[*]}"; then :; else
    git tag "${tag_args[@]}"
  fi
  if dry "git push ${REMOTE} ${VERSION}"; then :; else
    git push "${REMOTE}" "${VERSION}"
  fi
else
  echo "KAPRO_RELEASE_SKIP_TAG=true — skipping tag creation"
fi

# ---- watch the release workflow ---------------------------------------------

if [ "${SKIP_WATCH}" = "true" ]; then
  echo "KAPRO_RELEASE_SKIP_WATCH=true — exiting without watching"
  exit 0
fi

# Find the release workflow run for this tag. The tag push event may take a
# second to register; poll briefly.
run_id=""
for _ in $(seq 1 30); do
  run_id="$(gh run list --repo "${GH_NWO}" --workflow=Release --event=push \
    --branch "${VERSION}" --limit 1 --json databaseId --jq '.[0].databaseId' 2>/dev/null || true)"
  if [ -n "${run_id}" ] && [ "${run_id}" != "null" ]; then
    break
  fi
  sleep 2
done

if [ -z "${run_id}" ] || [ "${run_id}" = "null" ]; then
  abort "could not find a Release workflow run for tag ${VERSION} — check https://github.com/${GH_NWO}/actions"
fi

echo "watching release workflow run ${run_id}"
echo "  https://github.com/${GH_NWO}/actions/runs/${run_id}"

if ! gh run watch --repo "${GH_NWO}" --exit-status "${run_id}"; then
  abort "release workflow failed; see https://github.com/${GH_NWO}/actions/runs/${run_id}"
fi

# ---- published-artifact summary ---------------------------------------------

cat <<EOF

Release ${VERSION} published successfully.

GitHub Release:
  https://github.com/${GH_NWO}/releases/tag/${VERSION}

Published images:
  ghcr.io/kapro-dev/kapro-operator:${VERSION}
  ghcr.io/kapro-dev/kapro-cluster-controller:${VERSION}

Verify image signatures (cosign keyless):
  cosign verify ghcr.io/kapro-dev/kapro-operator:${VERSION} \\
    --certificate-identity-regexp="^https://github.com/${GH_NWO}" \\
    --certificate-oidc-issuer="https://token.actions.githubusercontent.com"

Verify SBOM attestation:
  cosign verify-attestation --type spdxjson \\
    ghcr.io/kapro-dev/kapro-operator:${VERSION} \\
    --certificate-identity-regexp="^https://github.com/${GH_NWO}" \\
    --certificate-oidc-issuer="https://token.actions.githubusercontent.com"

Verify SLSA build provenance:
  gh attestation verify oci://ghcr.io/kapro-dev/kapro-operator:${VERSION} \\
    --repo ${GH_NWO}

Verify Helm chart signature (after downloading chart + .sig + .pem):
  cosign verify-blob \\
    --signature kapro-operator-${VERSION#v}.tgz.sig \\
    --certificate kapro-operator-${VERSION#v}.tgz.pem \\
    --certificate-identity-regexp="^https://github.com/${GH_NWO}" \\
    --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \\
    kapro-operator-${VERSION#v}.tgz

Final smoke against published artifacts (run separately against a fresh cluster):
  kind create cluster --name kapro-release-verify --image kindest/node:v1.30.0
  KAPRO_RELEASE_VERSION=${VERSION} KAPRO_VERIFY_CLEANUP=true \\
    scripts/verify-install.sh release-cluster
  kind delete cluster --name kapro-release-verify

EOF
