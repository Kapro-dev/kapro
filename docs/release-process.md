# Release Process

Kapro releases are pre-stable until a stable Kubernetes API version is
published. Release tags still need the same discipline as a stable project:
clean source, current generated assets, explicit verification, and complete
release notes.

## Candidate Checklist

Run these checks from a clean checkout before tagging:

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

Run live checks when the environment has Docker, Kind, Argo CD, and Flux
available:

```bash
scripts/verify-install.sh kind-demo
scripts/verify-install.sh argo-e2e
scripts/verify-install.sh flux-git-e2e
scripts/verify-install.sh flux-e2e
```

The release candidate is not ready if:

- CRDs fail to render from Helm or Kustomize.
- Generated CRDs, Helm CRDs, embedded bootstrap CRDs, or RBAC drift.
- The operator image cannot start in Kind.
- The Kind demo does not create and converge `PromotionTarget` objects.
- Argo E2E cannot prove discover, adopt, Git-native source apply,
  `PromotionRun`, Argo sync, and `PromotionTarget.status.backendObjects`
  convergence.
- Flux Git-native E2E cannot prove common Flux source, HelmRelease, and
  Kustomize version fields can be updated safely.
- Live Flux E2E cannot prove generated Flux mappings drive real Flux controller
  reconciliation from `v1` to `v2`.
- `PluginRegistration` compatibility failures do not surface in status.
- Inline CEL gate denials do not block `PromotionRun` progression with clear
  status.
- PromotionTrigger signature policy failures do not surface in status
  conditions or Events.

## v0.4.0-alpha.0 Scope

Position `v0.4.0-alpha.0` as alpha production-capable for controlled adopters,
not as a GA stability promise. Use `CHANGELOG.md` for release notes and
`docs/ROADMAP.md` for the remaining GA exit criteria.

Include:

- Helm chart and Kustomize install paths.
- Local Kind demo.
- Promotion-domain APIs and examples.
- Argo and Flux brownfield onboarding.
- Inline CEL runtime guardrails and documented deferral to external policy
  systems for freeze-window enforcement.
- PromotionTrigger OCI signature verification policy.
- Plugin gateway preview with hot-loaded actuator, gate, and planner runtime
  registration.
- KPI planner contract, conformance preview, and runtime dispatch through the
  PromotionRun planner.
- Security, RBAC, operations, monitoring, conformance, API stability, and
  release notes.

Call out known limitations:

- All Kubernetes APIs are `kapro.io/v1alpha1`.
- The security model has not had an independent audit.
- Production soak across many customer repository styles is still limited.
- PromotionRun gate verification is not the artifact verification enforcement
  path yet; use PromotionTrigger signature policy.
- Docker dry-run checks may be optional for merging, but release candidates
  should publish a real multi-architecture operator image.

## Artifact Checklist

For a tagged release, publish:

- Git tag, for example `v0.4.0-alpha.0`.
- GitHub release notes generated from `CHANGELOG.md`.
- Operator image: `ghcr.io/kapro-dev/kapro-operator:<tag>`.
- Cosign signature for the operator image.
- Helm chart package: `kapro-operator-<version>.tgz`.
- Checksums for downloadable archives.

## Tagging

After verification and review:

```bash
git tag -a v0.4.0-alpha.0 -m "v0.4.0-alpha.0"
git push origin v0.4.0-alpha.0
```

The release workflow builds and pushes the operator image, signs it with
cosign, packages the Helm chart, and creates a GitHub release.

## Post-Release Checks

After the release is published:

1. Install the chart by tag or packaged artifact into a new Kind cluster.
2. Verify the operator image digest matches the signed image.
3. Confirm the README install or image override examples reference the released
   version.
4. Run the Kind demo against the released image, not only a locally built image.
5. Open follow-up issues for any release-note limitation that blocks adoption.
