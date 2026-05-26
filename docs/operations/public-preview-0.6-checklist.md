# Public Preview 0.6 Checklist

Use this checklist before tagging `v0.6.0`. It turns the roadmap into concrete
release evidence.

## CLI Contract

| User goal | Command | Release expectation |
|---|---|---|
| New direct apply repo | `kapro create direct ./promotion-repo --name checkout` | Works without Flux, Argo CD, or an OCI registry. |
| New Argo CD repo | `kapro create argo ./promotion-repo --name checkout` | Generates Argo `Application` starter files and Kapro promotion objects. |
| New Flux repo | `kapro create flux ./promotion-repo --name checkout` | Generates Flux starter files and Kapro promotion objects. |
| New OCI pull repo | `kapro create oci ./promotion-repo --name checkout` | Generates OCI pull substrate objects without making OCI the default path. |
| Existing Argo CD repo | `kapro import argo . --out ./kapro-connect --name checkout` | Starts observe-first and writes reviewable discovery files; `--adopt` grants write ownership only after review. |
| Existing Flux repo | `kapro import flux . --out ./kapro-connect --name checkout` | Starts observe-first and writes reviewable discovery files; `--adopt` grants write ownership only after review. |
| Generator/framework path | `kapro bootstrap generate --profile direct|argo|flux|oci` | Produces the same launch profile matrix used by create. |

`create` is the public command for new promotion repositories. `import` is the public
existing-GitOps command. `connect` and `discover` remain lower-level commands
for substrate-only scaffolds and inventory workflows.

## Required Evidence

- `make verify-local` passes on the release branch.
- `scripts/cli-scaffold-smoke.sh` passes and covers `direct`, `argo`, `flux`,
  existing Argo import, and existing Flux connect/import scaffolds.
- `KAPRO_CI_QUICKSTARTS=direct,flux,argo,oci scripts/ci-kind-smoke.sh` passes
  on a disposable kind cluster.
- `go run ./cmd/kapro-conformance all -o json` reports passing reference
  scenarios for `kubernetes-apply`, `argo`, `flux`, and `oci`.
- `docs/specs/substrate-parameter-spec.md` is published as the v1alpha1
  substrate author contract.
- The docs site includes quickstarts for `direct`, `argo`, `flux`, and `oci`.
- The release notes state that `runtime.kapro.io/v1alpha1` objects are
  controller-owned and not Git-authored.

## Product Boundaries

- Direct apply is the smallest default delivery path.
- OCI is a supported launch substrate, not a default dependency.
- Argo CD and Flux remain the local reconcilers for GitOps paths.
- Kapro does not build artifacts, provision clusters, store secrets, run CI, or
  act as a Helm registry.
- A standalone `kapro plan` command is not part of the 0.6 CLI contract unless
  it ships with a real dry-run/runtime contract; generated `Plan` manifests and
  `kapro lint` are the public-preview validation surface.

## Dogfood Gate

Before tagging, close or explicitly defer at least ten internal dogfood issues
from generated-repo and existing-GitOps adoption runs. Each issue should link
to the command that produced the finding and the evidence used to close it.
