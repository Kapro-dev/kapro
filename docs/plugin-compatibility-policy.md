# Plugin Compatibility Policy

Kapro plugin compatibility is decided by the contract version reported during
the plugin readiness probe. The plugin's own implementation version is recorded
for operators, but it is not used for compatibility decisions.

## Current Matrix

| Plugin type | Contract | Transport | Supported version | Conformance |
|---|---|---|---|---|
| `actuator` | KAI | gRPC | `v1alpha1` | `kapro-conformance actuator` or `conformance/actuator` |
| `gate` | KGI | gRPC | `v1alpha1` | `kapro-conformance gate` or `conformance/gate` |
| `planner` | KPI | gRPC | `v1alpha1` | `kapro-conformance planner` or `conformance/planner` |
| provider | KSP | Go SDK | `v1alpha1` capability metadata | `conformance/provider` |

The code source of truth for KAI, KGI, and KPI support is
`pkg/plugincompat/compatibility.go`. The current proto packages are:

- `kapro.io/kapro/spec/kai/v1alpha1`
- `kapro.io/kapro/spec/kgi/v1alpha1`
- `kapro.io/kapro/spec/kpi/v1alpha1`

KSP provider conformance is an in-process Go SDK contract, not a gRPC plugin
transport. Provider authors should import `kapro.io/kapro/conformance/provider`
from their own tests.

## Probe Behavior

When `KAPRO_ENABLE_PLUGIN_GATEWAY=true`, Kapro probes each `Plugin` endpoint by
calling `GetCapabilities`.

The probe accepts a plugin only when:

- `Plugin.spec.type` is `actuator`, `gate`, or `planner`;
- `Plugin.spec.protocol` is empty or `grpc`;
- the endpoint and TLS settings are valid;
- `GetCapabilities` returns a supported `contract_version`;
- planner plugins report at least one planner capability.

If a plugin omits `contract_version`, Kapro sets `Ready=False` and
`Compatible=False` with reason `MissingContractVersion`. If a plugin reports an
unknown version, Kapro sets `Ready=False` and `Compatible=False` with reason
`UnsupportedContractVersion`. The reported version is still copied to
`Plugin.status.contractVersion` so operators can see what the plugin attempted
to use.

Kapro stores `Plugin.status.schemaHash` from the accepted contract version and
capability list. If a later probe changes that shape, `SchemaChanged=True`
warns operators that the runtime contract advertised by the endpoint changed.

## Author Labels

Use these labels consistently:

| Label | Meaning |
|---|---|
| Kapro-compatible plugin | The plugin implements a supported contract version, passes the relevant base conformance suite, and documents backend assumptions. |
| Certified Kapro plugin | Reserved for a future governed certification process. Do not use this label yet. |

Conformance proves the base Kapro contract: idempotency, deterministic response
shape, capabilities, context cancellation, and request immutability where
applicable. It does not certify backend-specific production readiness.

## Badge Model

Plugin authors may add a badge or README line after running the relevant suite:

```text
Kapro-compatible: KAI v1alpha1, tested with kapro-conformance v0.5.5
```

For a Go provider:

```text
Kapro-compatible: KSP v1alpha1, tested with kapro.io/kapro/conformance/provider v0.5.5
```

Do not use "certified" language unless Kapro later publishes a certification
program with a separate review process.

## Deprecation And Removal

Future contract versions follow the general API stability policy:

- additive proto fields with new field numbers are compatible;
- removed proto fields remain reserved and are never reused;
- a replacement contract version ships with matching conformance coverage;
- Preview contract removals require release notes and at least one minor
  release of overlap when coexistence is safe;
- Stable contract removals wait for a major-version migration or a new API
  version.

Adding a contract version requires all of these changes in one release:

1. Add the version and `ContractPolicy` entry in `pkg/plugincompat`.
2. Update this policy and the plugin authoring guide.
3. Ship or update the matching conformance harness.
4. Preserve readiness failure messages for unsupported versions.

## Publishing Checklist

Before publishing a plugin:

- return the supported `contract_version` from `GetCapabilities`;
- return a clear plugin implementation version in `plugin_version`;
- run `kapro-conformance` or the Go conformance harness in CI;
- document required backend permissions and Kubernetes RBAC;
- document timeout, retry, and failure behavior;
- publish a tested `Plugin` manifest;
- avoid creating or mutating Kapro CRDs directly.
