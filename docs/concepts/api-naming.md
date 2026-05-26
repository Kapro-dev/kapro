# API Naming

Kapro's public delivery API separates class, configured instance, and
execution topology.

The preferred v1alpha1 shape is:

| Field | Meaning |
| --- | --- |
| `Substrate.spec.classRef.name` | The cluster-scoped `SubstrateClass` selected by this substrate instance. |
| `Substrate.spec.configRef` | A typed substrate-owned config object such as `ArgoCDSubstrateConfig` or `KubernetesApplyConfig`. |
| `Substrate.spec.execution.mode` | Where and how delivery runs. |

Example:

```yaml
apiVersion: kapro.io/v1alpha1
kind: Substrate
metadata:
  name: local-direct
spec:
  classRef:
    name: kubernetes-apply
  configRef:
    apiVersion: kubernetes.substrate.kapro.io/v1alpha1
    kind: KubernetesApplyConfig
    name: local-direct
  execution:
    mode: hub-push
```

`SubstrateClass` follows the Kubernetes class pattern used by
`StorageClass`, `IngressClass`, `GatewayClass`, and `RuntimeClass`.
Substrate-specific parameters belong in typed config CRDs owned by that
substrate package. Kapro core should not need to know the schema of an Argo CD,
Flux, KServe, webhook, or internal platform config.

The older open-string shape `spec.substrate.kind/actuator` was removed in the
0.6.2 public-preview reset. Custom substrates now use the same
`SubstrateClass` shape as built-in substrates, which avoids encoding the
selected substrate twice.

## Execution Modes

| Mode | Meaning |
| --- | --- |
| `hub-push` | The Kapro hub invokes the actuator directly. |
| `spoke-pull` | A cluster-side Kapro spoke pulls approved work and invokes the actuator near the target cluster. |
| `external-pull` | An external platform or plugin pulls approved Kapro decisions and reports status. |

Kapro uses one enum instead of separate `location` and `mode` fields because
combinations such as "hub pulls" are not meaningful for the public API.

## Removed Prototype Fields

Kapro 0.6.2 removes the oldest prototype fields and the interim open-string
substrate bridge:

| Removed field | Compatibility field | Preferred field |
| --- | --- | --- |
| `spec.driver` | `spec.substrate.kind` | `spec.classRef.name` |
| `spec.adapter` | `spec.substrate.actuator` | `spec.classRef.name` plus controller-owned class status |
| `spec.runtime` | `spec.execution.mode` | `spec.execution.mode` |

New manifests must use the preferred fields.

## Reference Fields

For new fields, and for the v0.7 cleanup of existing fields, Kapro follows one
reference rule:

```text
Only use a *Ref or *Refs suffix when the YAML value is an object reference, not
a bare string.
```

That gives users three predictable shapes:

| Shape | Use | Example |
| --- | --- | --- |
| Object reference | The target may need `name`, `namespace`, `apiVersion`, `kind`, or resolver metadata. | `configRef.name`, `pipelineRef.name`, `secretRef.name`, `sourceRef.kind/name`. |
| Local name | The API server already knows the target kind and scope, and the field is part of the core Kapro action vocabulary. | `unit: checkout`, `fleet: production`, `plan: progressive`. |
| Nested binding | The parent field already names the domain. | `spec.delivery.ref: flux`, not `spec.delivery.substrateRef`. |

Compatibility fields such as `unit: checkout`,
`fleet: checkout`, `plan: progressive`, and `sourceRef: catalog` remain
accepted in the v0.6.x line where they already exist. New examples and future
API versions should avoid adding scalar `*Ref` fields.

Selectors should use Kubernetes `LabelSelector` syntax (`matchLabels` and
`matchExpressions`) for new user-authored fields. A plain map selector is only
acceptable as an explicitly documented compatibility shorthand.
