# PromotionRun Process

This process is for early Kapro promotionruns. It favors explicit verification over
automation assumptions because the project is still stabilizing its install,
security, and plugin contracts.

## PromotionRun Candidate Checklist

Run these checks from a clean checkout before tagging:

```bash
git fetch origin
git switch main
git pull --ff-only
go test ./...
scripts/verify-install.sh render
```

For the local demo:

```bash
scripts/verify-install.sh kind-demo
```

For Argo brownfield readiness:

```bash
scripts/verify-install.sh argo-e2e
```

For Flux Git-native brownfield readiness:

```bash
scripts/verify-install.sh flux-git-e2e
```

For live Flux controller readiness:

```bash
scripts/verify-install.sh flux-e2e
```

The promotionrun candidate is not ready if:

- CRDs fail to render from Helm or Kustomize.
- The operator image cannot start in Kind.
- The demo does not create `PromotionTarget` objects.
- The Argo E2E cannot prove discover, adopt, Git-native source apply, PromotionRun,
  Argo sync, and `PromotionTarget.status.backendObjects` convergence.
- The Flux Git-native E2E cannot prove common Flux source, HelmRelease, and
  Kustomize version fields can be updated safely.
- The live Flux E2E cannot prove generated Flux mappings drive real Flux
  controller reconciliation from `v1` to `v2`.
- Production targets cannot be unblocked by the demo approvals.
- `PluginRegistration` compatibility conditions are missing from a probe
  failure or unsupported contract version test.
- PromotionTrigger signature policy failures do not surface in status
  conditions or Events.

## v0.1.0-alpha Scope

The first alpha should be positioned as alpha production-capable for controlled
adopters, not as a GA stability promise. Use
[Alpha Production Capability](alpha-production-capability.md) as the operator
contract for what is supported, what must be verified, and what remains below
GA. Use [GA Readiness](ga-readiness.md) for the evidence matrix before making
any stable production-readiness claim.

Include:

- Helm chart and Kustomize install paths.
- Local Kind demo.
- PromotionTrigger preview with OCI signature verification policy.
- Plugin gateway preview with hot-loaded actuator, gate, and planner runtime
  registration.
- KPI planner contract, conformance preview, and runtime dispatch through the
  promotionrun planner.
- Security, RBAC, multi-tenancy, operations, monitoring, conformance, and API
  stability docs.

Call out known limitations:

- All Kubernetes APIs are `v1alpha1`.
- The security model has not yet had an independent audit.
- Production soak across many customer repository styles is still limited.
- Docker dry-run checks may be optional for merging, but promotionrun candidates
  should still publish a real multi-architecture operator image.

## Artifact Checklist

For a tagged promotionrun, publish:

- Git tag, for example `v0.1.0-alpha.1`.
- GitHub release notes generated from `CHANGELOG.md`.
- Operator image:
  `ghcr.io/kapro-dev/kapro-operator:<tag>`.
- Cosign signature for the operator image.
- Helm chart package:
  `kapro-operator-<version>.tgz`.
- Checksums for downloadable archives.

For the `v0.1.0-alpha` promotionrun-specific checklist, use
[v0.1.0-alpha PromotionRun Runbook](promotionrun-v0.1.0-alpha.md).

## PromotionRun Notes Template

````markdown
# Kapro <version>

## What This PromotionRun Is

Short statement of maturity, intended audience, and supported install path.

## Highlights

- ...

## Install

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
```

## Upgrade

1. Back up Kapro CRDs and Secrets.
2. Apply CRDs.
3. Upgrade plugin servers and run conformance.
4. Roll the operator.
5. Watch PromotionRuns, PromotionTargets, and PluginRegistrations.

## Security Notes

- ...

## Compatibility

- Kapro APIs:
- KAI:
- KGI:
- KPI:

## Known Limitations

- ...

## Verification

- `go test ./...`
- `scripts/verify-install.sh render`
- `scripts/verify-install.sh kind-demo`
- `scripts/verify-install.sh argo-e2e`
- `scripts/verify-install.sh flux-git-e2e`
- `scripts/verify-install.sh flux-e2e`
````

## Post-PromotionRun Checks

After the promotionrun is published:

1. Install the chart by tag or packaged artifact into a new Kind cluster.
2. Verify the operator image digest matches the signed image.
3. Confirm the README install command references the released version.
4. Run the Kind demo against the released image, not only a locally built image.
5. Open follow-up issues for any promotionrun note limitation that blocks adoption.
