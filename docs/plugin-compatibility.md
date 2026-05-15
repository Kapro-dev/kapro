# Plugin Compatibility Policy

Kapro plugins declare two independent versions in `GetCapabilities`:

- `contract_version`: the KAI, KGI, or KPI wire contract implemented by the plugin.
- `plugin_version`: the plugin implementation version, owned by the plugin author.

Kapro uses `contract_version` for compatibility. `plugin_version` is recorded for
operators, but it is not used to decide whether a plugin can run.

## Compatibility Matrix

| Plugin type | Contract | Supported contract versions | Runtime status |
|---|---|---|---|
| `actuator` | KAI | `v1alpha1` | Probed; ready registrations can be loaded at startup when `KAPRO_ENABLE_PLUGIN_GATEWAY=true` |
| `gate` | KGI | `v1alpha1` | Probed; ready registrations can be loaded at startup when `KAPRO_ENABLE_PLUGIN_GATEWAY=true` |
| `planner` | KPI | `v1alpha1` | Probed and reported in status; runtime planner dispatch is future work |

The supported versions above are also defined in
`pkg/plugincompat/compatibility.go`.

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
