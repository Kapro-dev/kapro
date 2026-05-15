# Release Notes Guide

This guide defines the release-note structure for Kapro pre-stable releases.
Use it with `CHANGELOG.md` and `docs/api-stability.md` before tagging a release.

## Required Sections

Every release note should include these sections, even when the entry is
`None`:

- `Added` for new user-visible APIs, docs, commands, controllers, or examples.
- `Changed` for behavior, defaults, validation, schema, packaging, or docs that
  can affect existing users.
- `Deprecated` for fields, enum values, Go APIs, proto fields, commands, or
  workflows that remain available but should be replaced.
- `Removed` for deleted fields, enum values, Go APIs, proto fields, commands,
  or workflows.
- `Migration` for required user action during install or upgrade.
- `Compatibility` for CRD schema, plugin contract, lifecycle event, chart, and
  downgrade expectations.
- `Known Gaps` for preview limitations that operators must understand.

## Template

```markdown
## vX.Y.Z[-alpha.N] - YYYY-MM-DD

### Added

- ...

### Changed

- ...

### Deprecated

- Deprecated `<surface>` in favor of `<replacement>`. First deprecated in
  `<version>`; earliest removal is `<version>`; user action is `<action>`.

### Removed

- ...

### Migration

- Apply CRDs before rolling the operator.
- Run `<conformance package>` before enabling `<plugin image or contract>`.
- Update manifests from `<old field/workflow>` to `<new field/workflow>`.

### Compatibility

- CRD schema: `<compatible|breaking>`, because `<reason>`.
- Plugin contracts: `<compatible|breaking>`, because `<reason>`.
- Lifecycle events: `<compatible|breaking>`, because `<reason>`.
- Downgrade: `<allowed version range or not supported>`.

### Known Gaps

- ...
```

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
- Helm and manifests: CRD/RBAC/operator ordering is documented when it changes.

## v0.1.0-alpha Release Checklist

- [ ] Confirm the worktree is clean except intentional release edits.
- [ ] Confirm `CHANGELOG.md` has no empty required sections except `None`.
- [ ] Confirm `docs/api-stability.md` lists every shipped public surface.
- [ ] Run Go tests or record why they were skipped.
- [ ] Run markdown checks or record why they were unavailable.
- [ ] Confirm generated CRDs and proto stubs are current.
- [ ] Confirm release notes describe install, upgrade, downgrade, and known
      preview limitations.

## v0.2.0 Planning Checklist

- [ ] Promote only surfaces with conformance coverage, examples, and upgrade
      notes from Alpha to Preview.
- [ ] Add automated schema-diff or generated-CRD drift checks to release CI.
- [ ] Require a `Migration` release-note entry for every changed shipped
      example, default, CRD validation rule, or plugin contract.
- [ ] Add downgrade guidance whenever stored CRD schema or status changes.
- [ ] Decide whether any deprecated `v0.1.0-alpha` compatibility shims can be
      removed in `v0.2.0`; if so, reserve proto fields and document replacement
      workflows.
