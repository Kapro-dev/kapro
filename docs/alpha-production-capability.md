# Alpha Production Capability

Kapro's current target maturity is **alpha production-capable for controlled
adopters**, not GA.

This means the project should be usable by platform teams that can tolerate
`v1alpha1` API movement and that are willing to run explicit verification before
putting Kapro on a real fleet. It does not mean the APIs, upgrade path, plugin
runtime, or security posture are final.

## Supported Alpha Paths

The alpha production-capable scope includes these paths:

- Install Kapro with the Helm chart or rendered Kustomize manifests.
- Run the controller against the generated CRDs and RBAC.
- Connect Argo CD brownfield repositories with plain Applications,
  multi-source Applications, app-of-apps child Applications, and common
  ApplicationSet Git generator layouts.
- Connect Flux brownfield repositories with `GitRepository`, `OCIRepository`,
  `Bucket`, `Kustomization`, and `HelmRelease` mappings.
- Use observe-first discovery before any adopt/write mode.
- Use transactional Git-native source writes for discovered JSON, YAML,
  Kustomize image, Argo source, and Flux source fields.
- Use explicit RBAC roles for observe-only and adopt/write modes.
- Run KAI, KGI, and KPI plugin conformance before enabling external plugins.
- Verify PromotionTrigger signature policy behavior before enabling autonomous
  promotionrun creation.

## Required Verification

Before using a Kapro alpha on a real fleet, run these checks from a clean
checkout or from the promotionrun artifact being evaluated:

```bash
go test ./...
make build
make lint
make validate-yaml-json
make check-markdown-links
scripts/verify-install.sh render
scripts/verify-install.sh kind-demo
scripts/verify-install.sh argo-e2e
scripts/verify-install.sh flux-git-e2e
scripts/verify-install.sh flux-e2e
```

If a dependency such as Docker, Kind, Argo CD, or Flux cannot run in the
verification environment, record that waiver in the promotionrun issue and do not
claim that path as verified for the deployment.

## Operating Contract

Run Kapro alpha with these constraints:

- Start every brownfield integration in observe mode.
- Switch to adopt/write mode only after reviewing generated `PromotionSource`
  mappings and `BackendProfile.status.discovery`.
- Require explicit labels or annotations for objects Kapro may write.
- Keep Argo CD and Flux credentials owned by their existing systems; Kapro
  references backend configuration and does not copy those Secrets.
- Treat Kapro as the cross-cluster promotion coordinator. Local sync, rollout,
  traffic shifting, and workload health remain owned by Argo CD, Flux,
  Kubernetes, Argo Rollouts, Flagger, meshes, or external actuators.
- Use least-privilege RBAC: observe-only installs should not receive patch or
  update rights on backend objects.
- Keep the hub gateway and plugin gateway behind cluster networking and
  authentication controls.
- Use promotionrun-scoped approvals and gates for production waves.
- Monitor promotionrun stuck states, gate failure rates, plugin probe failures,
  blocked triggers, and rollout duration percentiles.

## Not GA Yet

These items intentionally keep the project below GA:

- Kubernetes APIs are still `kapro.io/v1alpha1`.
- No published upgrade history or conversion-webhook contract exists yet.
- Real-world soak is still limited across customer repository styles.
- The security model is documented, but it has not had an independent audit.
- Large-fleet behavior has synthetic and local E2E coverage, but not broad
  production soak across many operators and environments.

## Alpha Exit Criteria

Kapro should not be described as GA until these are true. The live status is
tracked in [GA Readiness](ga-readiness.md).

- Stable API versioning and upgrade policy are backed by promotionrun history.
- Conversion and migration paths exist for any breaking API change.
- Argo and Flux brownfield paths have repeatable live E2E coverage in CI or in
  a documented promotionrun gate.
- Scale limits are benchmarked and documented for repository size, backend
  object count, target count, and promotionrun fanout.
- Security boundaries, gateway authentication, plugin trust, and RBAC have had
  an external review.
- Multiple non-maintainer users have successfully installed, connected, and
  operated Kapro through at least one promotionrun cycle.
