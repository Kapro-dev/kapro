# RBAC and Tenancy Model

Kapro uses cluster-scoped CRDs because Promotions, PromotionRuns, fleet clusters, plugin
registrations, and approvals coordinate work across namespaces and clusters.
Tenancy is expressed through labels, admission policy, and narrowly scoped
ClusterRoles rather than by making the core APIs namespaced.

## Personas

| Persona | Owns | Typical permissions |
|---|---|---|
| Platform admin | Operator install, CRDs, controller flags, cluster-wide policy | Full admin on `kapro.io/*` and install namespace |
| Extension admin | External plugin endpoints and plugin credentials | Create/update `PluginRegistration`; read referenced plugin Secrets |
| Promotion manager | Promotion and trigger policy for one team or app | Create/update `Promotion`, `PromotionTrigger`, `PromotionPlan`, `PromotionSource` with team labels; read `PromotionRun` execution records |
| Approver | Human gate decisions for assigned teams/environments | Create `Approval`; read relevant `PromotionRun` and `PromotionTarget` status |
| Auditor | Evidence and status | Read-only on Kapro CRDs and Events |

## Ownership Labels

Every user-created Kapro object should carry these labels:

| Label | Required on | Meaning |
|---|---|---|
| `kapro.io/team` | `Promotion`, `PromotionTrigger`, `PromotionPlan`, `PromotionSource`, `Approval` | Owning team or service group |
| `kapro.io/environment` | `FleetCluster`, `PromotionPlan`, `Approval` | Environment boundary such as `dev`, `staging`, `prod` |
| `kapro.io/plugin-owner` | `PluginRegistration` | Team accountable for the plugin endpoint |

Admission policy should reject objects that omit the ownership labels in shared
clusters. Kapro treats these labels as authorization inputs for policy engines
such as Kubernetes ValidatingAdmissionPolicy, Kyverno, Gatekeeper, or the Kapro
webhook layer.

## Who Can Register Plugins?

Only platform admins and extension admins should create or update
`PluginRegistration` objects.

Plugin registration is a privileged action because an actuator plugin can change
delivery backend state and a gate plugin can unblock or block production
promotion. External plugins must run in a platform-controlled namespace, expose
TLS, and use a namespaced `spec.tlsSecretRef` when custom CA or mTLS material is
required.

Baseline rule:

- `create`, `update`, `patch`, `delete` on `pluginregistrations.kapro.io`:
  extension admins only.
- `get`, `list`, `watch` on `pluginregistrations.kapro.io`: Promotion managers
  and auditors may read status.
- Secrets referenced by `spec.tlsSecretRef`: readable only by the Kapro operator
  service account and the owning extension admin group.

## Who Can Create PromotionTriggers?

Promotion managers may create `PromotionTrigger` objects for their own team labels.
Production triggers should require platform review before being unsuspended.

Baseline rule:

- Teams may create suspended triggers with `spec.suspended: true`.
- Only Promotion managers for the matching `kapro.io/team` may update the trigger.
- Only production Promotion managers or platform admins may set
  `spec.suspended: false` for `kapro.io/environment=prod`.
- Registry credential Secrets referenced by `spec.source.oci.secretRef` must be
  namespaced and readable only by the Kapro operator service account.

`PromotionTrigger` creates or updates `Promotion` objects through the controller
service account; the `Promotion` controller then stamps `PromotionRun`
attempts. Admission should still validate that generated runtime objects keep
the same `kapro.io/team` and approved scope as the trigger.

## Who Can Approve Gates?

Approvers create `Approval` objects. The admission webhook fills
`spec.approvedBy` from request user info when it is empty, so users should not
be allowed to impersonate another approver by setting that field manually.

Baseline rule:

- Approvers may `create` `approvals.kapro.io`.
- Approvers may not update `Approval.status`; status is controller-owned.
- Bypass approvals require a separate emergency group and audit process.
- Production approvals should match both `kapro.io/team` and
  `kapro.io/environment` policy.

Kapro approvals are cluster-scoped and named deterministically:
`<promotionrun>-<ref>`. The `ref` binds the approval to one exact target FSM step,
which prevents one approval from unintentionally unblocking unrelated targets.

## Namespace and Team Boundaries

The operator runs in `kapro-system`. Plugin workloads, webhook backends, and
notification integrations should also live in platform-controlled namespaces,
not in application namespaces.

Recommended namespace pattern:

| Namespace | Contents |
|---|---|
| `kapro-system` | Kapro operator, webhook Service, operator-owned Secrets |
| `kapro-plugins` | External plugin Deployments and Services |
| `team-<name>` | Optional app-owned manifests and registry credentials |

For cluster-scoped Kapro objects, tenant isolation is enforced by:

- label selectors in RBAC aggregation or admission policy;
- immutable ownership labels after create;
- Secret refs that always include a namespace;
- approval policy that checks user group, team, environment, and bypass use;
- audit Events and notification sinks for every gate and approval transition.

## Example Role Split

The exact bindings are cluster-specific, but the intended split is:

```yaml
kind: ClusterRole
metadata:
  name: kapro-promotion-manager
rules:
  - apiGroups: ["kapro.io"]
    resources: ["promotions", "promotiontriggers", "promotionplans", "promotionsources"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: ["kapro.io"]
    resources: ["promotionruns", "promotiontargets", "fleetclusters", "pluginregistrations"]
    verbs: ["get", "list", "watch"]
---
kind: ClusterRole
metadata:
  name: kapro-approver
rules:
  - apiGroups: ["kapro.io"]
    resources: ["approvals"]
    verbs: ["get", "list", "watch", "create"]
  - apiGroups: ["kapro.io"]
    resources: ["promotionruns", "promotiontargets"]
    verbs: ["get", "list", "watch"]
---
kind: ClusterRole
metadata:
  name: kapro-extension-admin
rules:
  - apiGroups: ["kapro.io"]
    resources: ["pluginregistrations"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

These roles are intentionally broad examples. Production clusters should pair
them with admission checks for team and environment ownership.

## Backend Observe and Adopt RBAC

Brownfield backends should use different permissions for discovery and
promotion writes. The example roles in
`examples/rbac/backend-observe-adopt-roles.yaml` split those surfaces:

| Mode | Required access | Notes |
|---|---|---|
| Argo `Observe` | Read Argo cluster Secrets, Applications, and ApplicationSets in the Argo namespace. | No patch rights. Kapro reports selected, skipped, and unsupported objects in `BackendProfile.status`. |
| Argo `Adopt` | Patch selected Applications. | The built-in actuator writes only `spec.source.targetRevision`. ApplicationSet template writes require the ApplicationSet actuator plugin and separate RBAC. |
| Flux `Observe` | Read GitRepository, OCIRepository, HelmRelease, and Kustomization objects in the Flux namespace. | No patch rights. |
| Flux `Adopt` | Patch selected HelmRelease or Kustomization objects. | Bind per namespace and pair with admission or policy rules that enforce the team selector. |

Kubernetes RBAC cannot express label-selector-scoped patch permissions by
itself. In shared namespaces, combine these roles with admission policy or
separate namespaces per tenant so `managementPolicy: Adopt` cannot mutate
another team's backend objects.

For large Argo or Flux control planes, set `BackendProfile.spec.discovery.selector`
and keep `spec.discovery.maxObjects` near the default `1000`. If a backend list
exceeds that bound, Kapro marks discovery not ready instead of importing an
unreviewable set of objects. Raise the limit only after the selector expresses a
clear team or application boundary.
