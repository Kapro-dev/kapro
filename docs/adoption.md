# Adoption Guide

Kapro adoption starts by choosing who already owns delivery.

Kapro owns the promotion lifecycle: rollout intent, stage order, gates,
approvals, immutable attempts, and target evidence. Flux, Argo CD, OCI pull
agents, or plugins still own local reconciliation.

## Choose A Path

| Situation | Start with | What Kapro creates |
|---|---|---|
| New platform repo, Flux or spoke pull delivery | `kapro bootstrap greenfield ./promotion-repo --backend flux --mode pull --name checkout` | Backend, clusters, Fleet, Plan, Promotion, and Flux starter files. |
| New platform repo, Argo CD Applications already planned | `kapro bootstrap greenfield ./promotion-repo --backend argo --mode push --name checkout` | Backend, clusters, Fleet, Plan, Promotion, and Argo starter Application. |
| Existing Argo CD repo | `kapro bootstrap brownfield argo . --out ./kapro-connect --name checkout` | Observe-mode Backend, Source mappings, and discovery reports. |
| Existing Flux repo | `kapro bootstrap brownfield flux . --out ./kapro-connect --name checkout` | Observe-mode Backend, Source mappings, and discovery reports. |
| Outbound-only clusters without Flux or Argo CD | `kapro bootstrap greenfield ./promotion-repo --backend oci --mode pull --name checkout` | OCI Backend, clusters, Fleet, Plan, and Promotion skeleton. |

Use `kapro bootstrap guide` when you want the same decision tree in the
terminal.

Install the source-built CLI first when you are not working from a local
checkout. Bootstrap is available on `main`; use a tagged CLI release here once
one includes the bootstrap command.

```bash
git clone --branch main https://github.com/Kapro-dev/kapro.git
cd kapro
make build
export PATH="$PWD/bin:$PATH"
```

## Greenfield Flow

Greenfield means you want Kapro to scaffold the promotion repository shape.

```bash
kapro bootstrap greenfield ./promotion-repo \
  --backend flux \
  --mode pull \
  --name checkout
```

The generated repository has the first-use objects in dependency order:

```text
backends/
clusters/
plans/
fleets/
promotions/
```

Apply it after installing the operator:

```bash
kubectl apply --recursive -f ./promotion-repo
kubectl get fleets,plans,promotions,promotionruns,targets
```

The user-authored object is `Promotion`. The controller creates
`PromotionRun` and `Target` records.

## Brownfield Flow

Brownfield means Argo CD or Flux already owns applications, credentials, and
reconciliation. Kapro should start by observing and producing reviewable
mappings.

```bash
kapro bootstrap brownfield argo . \
  --out ./kapro-connect \
  --name checkout \
  --namespace argocd \
  --selector kapro.io/import=true,team=checkout
```

or:

```bash
kapro bootstrap brownfield flux . \
  --out ./kapro-connect \
  --name checkout \
  --namespace flux-system \
  --selector kapro.io/import=true,team=checkout
```

This generates:

- an observe-mode `Backend`;
- a `Source` mapping of deployable units to backend-native version fields;
- `discovery/*-discovery.yaml` with selected and skipped objects;
- `discovery/kapro-git-map.yaml` with write-target evidence.

Nothing is adopted yet. Review the generated files first. Switch
`Backend.spec.discovery.managementPolicy` from `Observe` to `Adopt` only after
the owning team approves exactly which fields Kapro may write.

For continuous in-cluster discovery, `kapro adopt argo-cd --apply` or
`kapro adopt flux --apply` creates or updates a `Backend` and matching
`AdapterPolicy`. The policy fails closed when the Backend is missing, discovery
is disabled, the policy adapter does not match the Backend adapter, or the
registered adapter cannot complete discovery. Use `--dry-run=client` with
`--apply` to validate the live writes without persisting resources.

## Promotion Flow

After greenfield scaffolding or brownfield mapping review, promotion looks the
same:

```bash
kapro promote checkout --version v1.2.3
kapro diag checkout-v1-2-3
kapro tree checkout-v1-2-3
```

`kapro promote` creates or updates durable `Promotion` intent. The controller
stamps immutable `PromotionRun` attempts and per-target `Target` records.

## Safety Defaults

- Brownfield output starts in `Observe`, not `Adopt`.
- Discovery scans tracked Git files and is bounded by file and unit limits.
- Git-native writes require explicit `kapro source apply`; Kapro does not push
  unless `--commit --push` is set.
- Live Argo CD Application writes require opt-in labels or annotations.
- OCI pull delivery uses two-phase staging: server-side dry-run apply for every
  object first, then commit only when the whole staging pass succeeds. The
  optional `spec.delivery.staging` API currently exposes this conservative
  `TwoPhase`/`Abort` contract without changing existing backend defaults.
- Vault bootstrap material is a preview contract. The built-in CSR bootstrap
  controller fails closed with `BootstrapVaultDisabled` rather than falling
  back to Kubernetes Secrets when `spec.bootstrap.materialSource.type=Vault`.
- Direct `PromotionRun` creation is advanced/debug usage, not the default user
  workflow.

## Validation

For local scaffolds:

```bash
kapro lint --strict ./promotion-repo/**/*.yaml
scripts/cli-scaffold-smoke.sh
```

For backend-specific proof:

```bash
scripts/verify-install.sh argo-e2e
scripts/verify-install.sh flux-git-e2e
scripts/verify-install.sh flux-e2e
```

The E2E scripts are heavier than the first 10-minute path. Use them before
claiming a production brownfield mapping is ready.
