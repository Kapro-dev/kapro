# API Naming

Kapro's public delivery API separates class, configured instance, and
execution topology.

The preferred v1alpha2 shape is:

| Field | Meaning |
| --- | --- |
| `Backend.spec.classRef.name` | The cluster-scoped `SubstrateClass` selected by this backend instance. |
| `Backend.spec.configRef` | A typed substrate-owned config object such as `ArgoCDSubstrateConfig` or `KubernetesApplyConfig`. |
| `Backend.spec.execution.mode` | Where and how delivery runs. |

Example:

```yaml
apiVersion: kapro.io/v1alpha2
kind: Backend
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

The older open-string shape remains accepted during the migration window:

| Field | Meaning |
| --- | --- |
| `spec.substrate.kind` | The delivery domain, such as `argo`, `flux`, `oci`, `webhook`, or a platform-owned value like `company-paas`. |
| `spec.substrate.actuator` | The concrete Kapro implementation or plugin for that domain. Optional for built-ins. |
| `spec.execution.mode` | Where and how delivery runs. |

Compatibility example:

```yaml
apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: hello-world
spec:
  substrate:
    kind: hello-world
    actuator: hello-world
  execution:
    mode: hub-push
```

`substrate.kind` remains intentionally open. Kapro validates the format with
the same lowercase DNS-style shape Kubernetes users expect, but it does not
restrict the value to a closed enum. New public examples should prefer
`classRef` plus `configRef`; the string shape is kept for existing manifests,
small demos, and migration.

## Execution Modes

| Mode | Meaning |
| --- | --- |
| `hub-push` | The Kapro hub invokes the actuator directly. |
| `spoke-pull` | A cluster-side Kapro spoke pulls approved work and invokes the actuator near the target cluster. |
| `external-pull` | An external platform or plugin pulls approved Kapro decisions and reports status. |

Kapro uses one enum instead of separate `location` and `mode` fields because
combinations such as "hub pulls" are not meaningful for the public API.

## Deprecated Compatibility Fields

Older manifests may still use:

| Deprecated field | New field |
| --- | --- |
| `spec.driver` | `spec.substrate.kind` |
| `spec.adapter` | `spec.substrate.actuator` |
| `spec.runtime` | `spec.execution.mode` |

Both shapes are accepted during the v0.x migration window, but one object must
not set both shapes. The deprecated fields are scheduled for removal in
`v0.5.20`.
