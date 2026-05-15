# RBAC and Tenancy Model

Kapro uses cluster-scoped CRDs because releases, member clusters, plugin
registrations, and approvals coordinate work across namespaces and clusters.
Tenancy is expressed through labels, admission policy, and narrowly scoped
ClusterRoles rather than by making the core APIs namespaced.

## Personas

| Persona | Owns | Typical permissions |
|---|---|---|
| Platform admin | Operator install, CRDs, controller flags, cluster-wide policy | Full admin on `kapro.io/*` and install namespace |
| Extension admin | External plugin endpoints and plugin credentials | Create/update `PluginRegistration`; read referenced plugin Secrets |
| Release manager | Release and trigger policy for one team or app | Create/update `Release`, `ReleaseTrigger`, `Pipeline`, `PromotionSource` with team labels |
| Approver | Human gate decisions for assigned teams/environments | Create `Approval`; read relevant `Release` and `ReleaseTarget` status |
| Auditor | Evidence and status | Read-only on Kapro CRDs and Events |

## Ownership Labels

Every user-created Kapro object should carry these labels:

| Label | Required on | Meaning |
|---|---|---|
| `kapro.io/team` | `Release`, `ReleaseTrigger`, `Pipeline`, `PromotionSource`, `Approval` | Owning team or service group |
| `kapro.io/environment` | `MemberCluster`, `Pipeline`, `Approval` | Environment boundary such as `dev`, `staging`, `prod` |
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
- `get`, `list`, `watch` on `pluginregistrations.kapro.io`: release managers
  and auditors may read status.
- Secrets referenced by `spec.tlsSecretRef`: readable only by the Kapro operator
  service account and the owning extension admin group.

## Who Can Create ReleaseTriggers?

Release managers may create `ReleaseTrigger` objects for their own team labels.
Production triggers should require platform review before being unsuspended.

Baseline rule:

- Teams may create suspended triggers with `spec.suspended: true`.
- Only release managers for the matching `kapro.io/team` may update the trigger.
- Only production release managers or platform admins may set
  `spec.suspended: false` for `kapro.io/environment=prod`.
- Registry credential Secrets referenced by `spec.source.oci.secretRef` must be
  namespaced and readable only by the Kapro operator service account.

`ReleaseTrigger` creates `Release` objects through the controller service
account. Admission should still validate that the generated Release keeps the
same `kapro.io/team` and approved scope as the trigger.

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
`<release>-<ref>`. The `ref` binds the approval to one exact target FSM step,
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
  name: kapro-release-manager
rules:
  - apiGroups: ["kapro.io"]
    resources: ["releases", "releasetriggers", "pipelines", "promotionsources"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: ["kapro.io"]
    resources: ["releasetargets", "memberclusters", "pluginregistrations"]
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
    resources: ["releases", "releasetargets"]
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
