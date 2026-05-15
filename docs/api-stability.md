# API Stability

Kapro uses Kubernetes API versioning plus feature-level maturity labels. The
current public API group is `kapro.io/v1alpha1`.

## Maturity Levels

| Level | Meaning | Compatibility promise |
|---|---|---|
| Alpha | Early API shape; feedback expected | May change between minor releases |
| Preview | Usable behind an explicit enablement or narrow scope | Field names are treated as sticky, behavior may still tighten |
| Stable | Default production path | Backward-compatible within the same API version |

## Current Surface

| Surface | Status |
|---|---|
| `Release`, `ReleaseTarget`, `Pipeline`, `MemberCluster`, `KaproApp` | Alpha core API |
| Built-in gates: soak, metrics, approval, verification, CEL, Job, webhook | Alpha core behavior |
| Lifecycle webhook notifications | Alpha |
| `Approval` CRD and admission mutation | Alpha |
| `ReleaseTrigger` | Preview |
| `PluginRegistration` | Preview |
| KAI, KGI, KPI protobuf contracts | Preview |
| External actuator and gate runtime dispatch | Preview, opt-in with `KAPRO_ENABLE_PLUGIN_GATEWAY=true` |
| External planner runtime dispatch | Future work; KPI conformance exists |

## Deprecation Policy

For `v1alpha1`, incompatible changes are allowed, but they must be documented in
`CHANGELOG.md` and migration notes when user data is affected.

Once a surface is marked stable:

- fields are not removed from the served API version;
- behavior changes preserve existing valid objects where possible;
- replacements are introduced before old fields are deprecated;
- deprecated fields remain for at least two minor releases;
- conversion or migration guidance is documented before removal in a later API
  version.

## Upgrade Policy

Operators should upgrade in this order:

1. Apply new CRDs.
2. Roll the controller Deployment.
3. Roll plugin Deployments.
4. Update `PluginRegistration` objects only after plugin probes pass.
5. Update ReleaseTrigger policy last.

Kapro controllers must tolerate older objects that omit newly added optional
fields. Required-field additions must happen through a new API version or be
guarded by admission defaults.

## Compatibility Rules

Kapro should reject or mark unsupported external contracts using Kubernetes
status conditions rather than failing silently.

Expected condition pattern:

| Condition | Resource | Meaning |
|---|---|---|
| `Ready=False,Reason=UnsupportedContractVersion` | `PluginRegistration` | Plugin reported a KAI/KGI/KPI contract outside the supported range |
| `Stalled=True,Reason=UnsupportedContractVersion` | `PluginRegistration` | Registration cannot be used until the plugin is upgraded or downgraded |
| `Reconciling=True,Reason=ProbePending` | `PluginRegistration` | Compatibility has not been established yet |

The supported contract matrix is maintained with plugin authoring docs. Until a
stable API version exists, `v1alpha1` is the only accepted contract version for
KAI, KGI, and KPI.
