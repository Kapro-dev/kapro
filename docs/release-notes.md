# Release Notes Guide

Use this guide with [`CHANGELOG.md`](../CHANGELOG.md) and
[`api-stability.md`](api-stability.md) before tagging a Kapro release.

## Required Sections

Every release note should include these sections, even when the entry is
`None`:

- `What This Release Is` for maturity, audience, and install path.
- `Highlights` for the most important user-visible changes.
- `Install` for chart, image, CRD, and namespace instructions.
- `Upgrade` for ordered user actions.
- `Security Notes` for trust boundary, RBAC, signature, and plugin notes.
- `Compatibility` for CRD schema, plugin contracts, lifecycle events, chart
  version, and downgrade expectations.
- `Migration` for required manifest or workflow changes.
- `Known Limitations` for alpha or preview limitations.
- `Verification` for commands that passed before tagging.

## Template

````markdown
# Kapro <version>

## What This Release Is

Short maturity statement, intended audience, and supported install path.

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
2. Apply CRDs and RBAC.
3. Upgrade plugin servers and run conformance.
4. Roll the operator.
5. Watch PromotionRuns, PromotionTargets, PromotionPolicies, and
   PluginRegistrations.

## Security Notes

- ...

## Compatibility

- Kapro APIs:
- KAI:
- KGI:
- KPI:
- Lifecycle events:
- Downgrade:

## Migration

- ...

## Known Limitations

- ...

## Verification

- `go test ./...`
- `make build`
- `make lint`
- `make validate-yaml-json`
- `make check-markdown-links`
- `scripts/verify-install.sh render`
- `scripts/verify-install.sh kind-demo`
- `scripts/verify-install.sh argo-e2e`
- `scripts/verify-install.sh flux-git-e2e`
- `scripts/verify-install.sh flux-e2e`
````

## Compatibility Review

Before tagging, review every change that touches these surfaces:

- CRDs: fields are additive, safely defaulted, or called out as breaking.
- Status: new fields and conditions are additive; old status meanings are not
  reused.
- Protos: new fields use new numbers; removed fields are reserved.
- Go extension packages: method signatures are unchanged or migration notes name
  the adapter.
- Lifecycle events: event type names and documented payload fields remain
  compatible.
- Examples: changed examples include migration notes for existing users.
- Helm and manifests: CRD, RBAC, webhook, and operator ordering is documented
  when it changes.

## v0.4.0-alpha.0 Checklist

- [ ] Confirm the worktree is clean except intentional release edits.
- [ ] Confirm `CHANGELOG.md` has a complete `v0.4.0-alpha.0` section.
- [ ] Confirm `docs/api-stability.md` lists every shipped public surface.
- [ ] Confirm release-facing docs do not link deleted planning/runbook pages.
- [ ] Confirm generated CRDs, Helm CRDs, embedded CRDs, and RBAC are current.
- [ ] Run Go tests or record why they were skipped.
- [ ] Run install verification or record environment waivers.
- [ ] Confirm release notes describe install, upgrade, downgrade, and known
      preview limitations.
