# Clean-Clone Install Verification

Use this checklist from a fresh clone before publishing or announcing a release.
It separates render checks, which do not need a cluster, from live install checks.

## Fresh Clone

```bash
tmpdir="$(mktemp -d)"
git clone https://github.com/Kapro-dev/kapro "${tmpdir}/kapro"
cd "${tmpdir}/kapro"
```

Check out the release tag or candidate branch you are validating:

```bash
git checkout v0.1.0
```

## Render Verification

Run the repository-local install verifier:

```bash
scripts/verify-install.sh render
```

This verifies that:

- Helm chart CRDs match `config/crd/bases`.
- `helm lint charts/kapro-operator` passes.
- `helm template ... --include-crds` renders.
- `kubectl kustomize config/default` renders.

## Live Cluster Verification

Use a disposable cluster or namespace. For Kind, create a cluster first:

```bash
kind create cluster --name kapro-install-verify
kubectl config use-context kind-kapro-install-verify
```

Install with webhooks disabled unless cert-manager is already installed:

```bash
scripts/verify-install.sh cluster
```

For a prerelease image that is not the chart default, override the image:

```bash
KAPRO_IMAGE_REPOSITORY=ghcr.io/kapro-dev/kapro-operator \
KAPRO_IMAGE_TAG=v0.1.0 \
scripts/verify-install.sh cluster
```

To remove the Helm release and namespace after verification:

```bash
KAPRO_VERIFY_CLEANUP=true scripts/verify-install.sh cluster
```

Expected result:

- `deployment/kapro-kapro-operator` rolls out in `kapro-system`.
- Kapro CRDs are present.
- `deploy`, `svc`, and `sa` exist in `kapro-system`.
- The operator service account can read `promotionruns.kapro.io`.

Delete a disposable Kind cluster when done:

```bash
kind delete cluster --name kapro-install-verify
```

## Demo Validation

The local demo exercises a PromotionRun through target planning, canary, manual
approval, and fixture-backed Flux convergence:

```bash
scripts/verify-install.sh kind-demo
```

This is intentionally heavier than render verification because it builds a
local operator image, creates a Kind cluster, runs the demo, approves production,
prints status, and deletes the cluster.

## Argo Brownfield E2E

The Argo E2E is the production-readiness check for brownfield Argo onboarding:

```bash
scripts/verify-install.sh argo-e2e
```

It creates a disposable Kind cluster, installs real Argo CD, installs Kapro,
creates an in-cluster Git server, runs `kapro adopt argo`, applies the generated
`BackendProfile` and `PromotionSource`, promotes Git-backed Argo mappings to
`v2`, creates a Kapro `PromotionRun`, and waits for Argo Applications plus
`PromotionTarget.status.backendObjects` to converge.

The fixture covers:

- a plain Argo `Application`;
- a multi-source Argo `Application` using `spec.sources[0].targetRevision`;
- an `ApplicationSet`-generated child `Application`;
- an `ApplicationSet` backed by a YAML Git file generator input;
- an app-of-apps root with a child `Application`.

By default `scripts/verify-install.sh argo-e2e` deletes the Kind cluster after a
successful run. To inspect resources afterward:

```bash
KAPRO_ARGO_E2E_CLEANUP=false scripts/argo-e2e.sh run
scripts/argo-e2e.sh status
scripts/argo-e2e.sh down
```

## Flux Git-Native E2E

The Flux Git-native E2E verifies Kapro can update common brownfield Flux
version fields without taking over Flux reconciliation:

```bash
scripts/verify-install.sh flux-git-e2e
```

The fixture covers:

- `GitRepository.spec.ref.tag`;
- `OCIRepository.spec.ref.tag`;
- `HelmRelease.spec.chart.spec.version`;
- HelmRelease image tag values;
- Kustomize `images[].newTag`;
- Helm `Chart.yaml` `version` and `appVersion`.

## Live Flux Controller E2E

The live Flux E2E proves the generated mapping can drive real Flux
reconciliation, not just local file mutation:

```bash
scripts/verify-install.sh flux-e2e
```

It creates a disposable Kind cluster, installs Flux controllers, serves a Git
fixture inside the cluster, bootstraps Flux from that repo, runs
`kapro adopt flux`, applies the generated `PromotionSource` from `v1` to `v2`,
pushes the Git change, and waits for Flux to reconcile the workload ConfigMap
to `v2`.

By default the wrapper deletes the Kind cluster after success. To inspect the
cluster afterward:

```bash
KAPRO_FLUX_E2E_CLEANUP=false scripts/flux-e2e.sh run
scripts/flux-e2e.sh status
scripts/flux-e2e.sh down
```
