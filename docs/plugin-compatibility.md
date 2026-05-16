# Plugin Compatibility Policy

Kapro plugins declare two independent versions in `GetCapabilities`:

- `contract_version`: the KAI, KGI, or KPI wire contract implemented by the plugin.
- `plugin_version`: the plugin implementation version, owned by the plugin author.

Kapro uses `contract_version` for compatibility. `plugin_version` is recorded for
operators, but it is not used to decide whether a plugin can run.

## Compatibility Matrix

| Plugin type | Contract | Supported contract versions | Conformance package | Example | Runtime status |
|---|---|---|---|---|---|
| `actuator` | KAI | `v1alpha1` | `conformance/actuator` | `examples/plugins/argocd-actuator` | Probed; ready registrations are hot-loaded when `KAPRO_ENABLE_PLUGIN_GATEWAY=true` |
| `gate` | KGI | `v1alpha1` | `conformance/gate` | `examples/plugins/slo-gate` | Probed; ready registrations are hot-loaded when `KAPRO_ENABLE_PLUGIN_GATEWAY=true` |
| `planner` | KPI | `v1alpha1` | `conformance/planner` | `examples/plugins/capacity-planner` | Probed; ready registrations are hot-loaded into release planning when `KAPRO_ENABLE_PLUGIN_GATEWAY=true` |

The supported versions above are also defined in
`pkg/plugincompat/compatibility.go`.

## Kapro-Compatible Plugins

A plugin may be described as Kapro-compatible when it:

- implements one supported KAI, KGI, or KPI gRPC contract;
- reports the supported `contract_version` from `GetCapabilities`;
- passes the matching conformance harness;
- documents required parameters, backend permissions, and runtime assumptions;
- leaves release state, retries, failure policy, and `ReleaseTarget` binding to
  Kapro.

Kapro-compatible is a contract claim, not a project endorsement.

## Probe Policy

The operator probes `GetCapabilities` before marking a `PluginRegistration`
ready. A plugin is not ready when:

- `contract_version` is empty;
- `contract_version` is not listed in the compatibility matrix for that plugin type;
- the endpoint cannot be dialed or `GetCapabilities` fails;
- a planner plugin does not report at least one planner capability: `filter`,
  `score`, `order`, or `defer`.

For missing or unsupported contract versions, the probe writes:

- `status.ready=false`;
- `status.contractVersion` with the reported value when one was provided;
- `Ready=False` with reason `MissingContractVersion` or
  `UnsupportedContractVersion`;
- `Compatible=False` with the same reason and a message listing supported
  versions;
- `Stalled=True`.

For dial or probe failures where the contract cannot be read, `Compatible` is
set to `Unknown`.

## Versioning Rules

Kapro accepts only explicitly listed contract versions. Future contract versions
must be added to the matrix and to `pkg/plugincompat` before the operator marks
plugins using them as ready.

Within a supported `v1alpha1` contract, changes must be backward-compatible:

- new fields must be optional for existing plugins;
- existing fields, enum meanings, and RPC names must not be repurposed;
- removing fields or changing required behavior requires a new contract version.

Plugin authors should run the matching conformance harness under
`conformance/actuator`, `conformance/gate`, or `conformance/planner` before
publishing a plugin.

## Certified Plugin Future

Certified Kapro plugin is a future ecosystem label. The expected bar is higher
than Kapro-compatible and is likely to include:

- passing conformance for each supported Kapro and contract version;
- signed release artifacts or equivalent provenance;
- a published support window and upgrade policy;
- documented compatibility ranges for Kapro, the plugin image, and the backend;
- reproducible registration manifests and operational limits.

Until that certification process exists, plugin authors should use
Kapro-compatible for plugins that meet the contract and conformance bar.
