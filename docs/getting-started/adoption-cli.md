# Adoption CLI

Kapro's adoption CLI is designed for teams that already know Argo CD or Flux
but do not want to learn every Kapro object before trying promotion workflows.

## First command

For a new repo, start with:

```bash
kapro create direct ./promotion-repo --name checkout
```

`direct` is the smallest no-extra-dependency path: no OCI registry, Flux
install, or Argo CD install is required for the generated repo shape.

Use Flux when clusters should pull desired state from inside their own network
boundary. Use Argo CD when Argo already owns Applications and Kapro should
promote versions through that existing control plane. Use OCI only when spokes
must pull OCI artifacts directly without Argo CD or Flux:

```bash
kapro create flux ./promotion-repo --name checkout
kapro create argo ./promotion-repo --name checkout
kapro create oci ./promotion-repo --name checkout
```

`kapro bootstrap generate` is the lower-level generator command behind the
same profile matrix:

```bash
kapro bootstrap generate ./promotion-repo --profile direct --name checkout
kapro bootstrap generate ./promotion-repo --profile argo --name checkout
kapro bootstrap generate ./promotion-repo --profile flux --name checkout
kapro bootstrap generate ./promotion-repo --profile oci --name checkout
```

## Existing GitOps repos

For an existing Argo CD or Flux repository, use observe-first adoption:

```bash
kapro import argo . --out ./kapro-connect --name checkout
kapro import flux . --out ./kapro-connect --name checkout
```

Observe-first adoption generates a Substrate, a DeliveryUnit with source
mappings, and discovery review files. It does not mutate live Argo CD or Flux
objects and it does not push Git changes. After review, pass `--adopt` only
when Kapro should manage the reviewed fields.

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

## Command Map

| Goal | Public command | Notes |
|---|---|---|
| Try Kapro in a new repo | `kapro create direct|argo|flux|oci` | Fast path with opinionated defaults. |
| Generate from an explicit profile | `kapro bootstrap generate --profile direct|argo|flux|oci` | Generator/framework surface used by docs, CI, and future template targets. |
| Connect an existing GitOps repo | `kapro import argo|flux` | Observe-first output with DeliveryUnit source mappings and discovery reports. |
| Create only observe-mode Substrate files | `kapro connect argo|flux` | Substrate-only scaffold for platform teams that want to wire discovery by hand. |
| Inventory without importing | `kapro discover argo|flux` | Lower-level diagnostic command used by `import`. |
