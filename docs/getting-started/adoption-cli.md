# Adoption CLI

Kapro's adoption CLI is designed for teams that already know Argo CD or Flux
but do not want to learn every Kapro object before trying promotion workflows.

## First command

For a new repo, start with:

```bash
kapro bootstrap generate ./promotion-repo --profile direct --name checkout
```

Use Flux when clusters should pull desired state from inside their own network
boundary. Use Argo CD when Argo already owns Applications and Kapro should
promote versions through that existing control plane:

```bash
kapro bootstrap generate ./promotion-repo --profile flux --name checkout
kapro bootstrap generate ./promotion-repo --profile argo --name checkout
```

Use OCI only when spokes must pull OCI artifacts directly without Argo CD or
Flux:

```bash
kapro quickstart oci ./promotion-repo --name checkout
```

## Existing GitOps repos

For an existing Argo CD or Flux repository, use observe-first adoption:

```bash
kapro adopt argo . --out ./kapro-connect --name checkout
kapro adopt flux . --out ./kapro-connect --name checkout
```

Observe-first adoption generates Substrate, Source, and discovery review files.
It does not mutate live Argo CD or Flux objects and it does not push Git
changes.

## Samples

Generate opinionated layouts when you want a concrete starting point:

```bash
kapro sample single-cluster ./sample
kapro sample dev-stage-prod ./sample
kapro sample multi-region ./sample
kapro sample argo-app-of-apps ./sample
kapro sample flux-monorepo ./sample
```

## Preflight and explanation

After installing the chart, run:

```bash
kapro doctor
```

`kapro doctor` checks CRDs, operator readiness, admission webhooks, RBAC, pull
secrets, and configured GitOps substrates.

When a promotion is waiting or blocked, run:

```bash
kapro explain <promotionrun>
```

`kapro explain` is the adoption-friendly alias for `kapro why`. It reads
DecisionTrace records and summarizes what gate, approval, target, or delivery
step explains the current state.

## Push and pull

Kapro exposes only one delivery distinction during onboarding:

- `pull`: each cluster pulls desired state from inside its own network
  boundary.
- `push`: the hub promotes desired versions through a substrate such as Argo CD.

Argo CD and Flux still own local sync and rollout mechanics. Kapro adds the
promotion intent, gates, approvals, and audit trail across clusters.
