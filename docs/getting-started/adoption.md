# Adoption Guide

Kapro adoption starts by choosing who already owns delivery.

Kapro owns the promotion lifecycle: rollout intent, stage order, gates,
approvals, immutable attempts, and target evidence. Flux, Argo CD, OCI pull
agents, or plugins still own local reconciliation.

## Choose A Path

| Situation | Start with | What Kapro creates |
|---|---|---|
| New repo, direct Kubernetes apply | `kapro create direct ./promotion-repo --name checkout` | `kubernetes-apply` class/config, Substrate, raw YAML, clusters, DeliveryUnit, Fleet, Plan, and Promotion scaffold. |
| New repo, Flux or spoke pull delivery | `kapro create flux ./promotion-repo --name checkout` | Flux class/config, Substrate, clusters, DeliveryUnit, Fleet, Plan, Promotion, and Flux starter files. |
| New repo, Argo CD Applications already planned | `kapro create argo ./promotion-repo --name checkout` | Argo CD class/config, Substrate, clusters, DeliveryUnit, Fleet, Plan, Promotion, and Argo starter Application. |
| Existing Argo CD repo | `kapro import argo . --out ./kapro-connect --name checkout` | Observe-mode Substrate, DeliveryUnit source mappings, and discovery reports. |
| Existing Flux repo | `kapro import flux . --out ./kapro-connect --name checkout` | Observe-mode Substrate, DeliveryUnit source mappings, and discovery reports. |
| Outbound-only clusters that must pull OCI artifacts | `kapro create oci ./promotion-repo --name checkout` | OCI Substrate, clusters, DeliveryUnit, Fleet, Plan, and Promotion skeleton. |

Use `kapro bootstrap guide` when you want the same decision tree in the
terminal.

For the adoption-first CLI tour, including `kapro sample`, `kapro doctor`, and
`kapro explain`, see [Adoption CLI](adoption-cli.md).

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
kapro create direct ./promotion-repo --name checkout
```

Other public-preview profiles use the same command:

```bash
kapro create argo ./promotion-repo --name checkout
kapro create flux ./promotion-repo --name checkout
kapro create oci ./promotion-repo --name checkout
```

`kapro bootstrap generate` exposes the same `direct`, `argo`, `flux`, and `oci`
profiles for generator and template work when you need a lower-level command.

The generated repository has the first-use objects in dependency order:

```text
substrates/
apps/ or substrate-native starter manifests/
clusters/
deliveryunits/
plans/
fleets/
promotions/
```

Generated public-preview profiles use `Substrate.spec.classRef`, so keep the
default `substrateclass` and `substrate` controllers enabled, apply the
substrate first, and wait for readiness before applying generated clusters:

```bash
kubectl apply -f ./promotion-repo/substrates/direct.yaml
kubectl wait --for=condition=Ready substrate/direct --timeout=90s
kubectl apply --recursive \
  -f ./promotion-repo/apps \
  -f ./promotion-repo/clusters \
  -f ./promotion-repo/deliveryunits \
  -f ./promotion-repo/plans \
  -f ./promotion-repo/fleets \
  -f ./promotion-repo/promotions
kubectl get deliveryunits.kapro.io,fleets.kapro.io,plans.kapro.io,promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
```

`DeliveryUnit`, `Fleet`, and `Plan` are durable setup intent. `Promotion` is the
explicit rollout action. The controller creates `PromotionRun` and `Target`
records.

## Existing GitOps Adoption Flow

Use this when Argo CD or Flux already owns applications, credentials, and
reconciliation. Kapro should start by observing and producing reviewable
mappings; it should not take over writes during the first command.

```bash
kapro import argo . \
  --out ./kapro-connect \
  --name checkout \
  --namespace argocd \
  --selector kapro.io/import=true,team=checkout
```

or:

```bash
kapro import flux . \
  --out ./kapro-connect \
  --name checkout \
  --namespace flux-system \
  --selector kapro.io/import=true,team=checkout
```

This generates:

- an observe-mode `Substrate`;
- a `DeliveryUnit` mapping deployable units to substrate-native version fields;
- `discovery/review-summary.yaml` with adoption-readiness counts and next
  actions;
- `discovery/*-discovery.yaml` with selected and skipped objects;
- `discovery/kapro-git-map.yaml` with write-target evidence.

Nothing is adopted yet. Review the generated files first. After the owning team
approves exactly which fields Kapro may write, rerun the import with `--take`
or switch `Substrate.spec.discovery.managementPolicy` from `Observe` to
`Adopt`.

For continuous in-cluster discovery, `kapro import argo --apply` or
`kapro import flux --apply` creates or updates a `Substrate` and matching
`SubstrateDiscoveryPolicy`. The policy fails closed when the Substrate is missing, discovery
is disabled, the policy adapter does not match the Substrate adapter, or the
registered adapter cannot complete discovery. Use `--dry-run=client` with
`--apply` to validate the live writes without persisting resources. Add
`--take` only after review when the live `Substrate` should move to `Adopt`.
Run the operator with `substrate` and `substratediscoverypolicy` controllers
when using this live apply path.

## Promotion Flow

After greenfield scaffolding or existing GitOps mapping review, promotion looks
the same:

```bash
kapro promote checkout --version v1.2.3
# Existing GitOps import output does not guess your target set; pass --fleet
# until you add spec.defaultFleetRef to the generated DeliveryUnit.
kapro promote checkout --version v1.2.3 --fleet checkout-prod
kapro diag checkout-v1-2-3
kapro tree checkout-v1-2-3
```

`kapro promote` creates or updates durable `Promotion` intent. The controller
stamps immutable `PromotionRun` attempts and per-target `Target` records.

## Safety Defaults

- Existing GitOps adoption output starts in `Observe`, not `Adopt`.
- Discovery scans tracked Git files and is bounded by file and unit limits.
- Git-native writes require explicit `kapro source apply`; Kapro does not push
  unless `--commit --push` is set.
- Live Argo CD Application writes require opt-in labels or annotations.
- OCI pull delivery uses two-phase staging: server-side dry-run apply for every
  object first, then commit only when the whole staging pass succeeds. The
  optional `spec.substrate.staging` API currently exposes this conservative
  `TwoPhase`/`Abort` contract without changing existing substrate defaults. This
  is validation-atomic before commit, not a Kubernetes transactional rollback:
  commit-phase infrastructure failures are reported and retried rather than
  undone destructively.
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

For substrate-specific proof:

```bash
scripts/verify-install.sh argo-e2e
scripts/verify-install.sh flux-git-e2e
scripts/verify-install.sh flux-e2e
```

The E2E scripts are heavier than the first 10-minute path. Use them before
claiming a production existing-GitOps mapping is ready.
