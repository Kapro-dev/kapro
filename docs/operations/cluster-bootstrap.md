# Registering a Cluster (Pull Mode)

This guide walks through the pull-mode registration flow: installing
`kapro-cluster-controller` on a workload cluster so it self-registers with a
running Kapro hub via a CSR-based handshake.

For hub-driven push mode, use `kapro spoke add`; pull mode should use the
bootstrap flow below.

## Prerequisites

- A running Kapro hub (the `kapro-operator` chart installed) on a cluster
  reachable from the spoke.
- `kubectl` context pointed at the **hub** for steps 1–2.
- `kubectl` context pointed at the **spoke** for step 3.
- Helm 3 and the `kapro-cluster-controller` chart package from the Kapro
  GitHub Release. For source-checkout development, use
  `charts/kapro-cluster-controller` instead.
- The `kapro` CLI built from this repo (`go build ./cmd/kapro`).

The hub's `ClusterBootstrapReconciler` is a preview controller and is not
enabled by the default ADR-0010 core install. Enable it on the hub before
running this flow, and set `hubAPIURL` to the hub API server URL reachable from
spokes:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set hubAPIURL=https://hub.example.com:6443 \
  --set controllers='{deliveryunit,fleet,plan,promotion,promotionrun,cluster,cluster-bootstrap}'
```

## Step 1 — Generate values + bootstrap Secret on the hub

```bash
kapro spoke bootstrap de-prod-01 \
  --hub-url https://hub.example.com:6443 \
  --secret-out /tmp/de-prod-01-bootstrap-secret.yaml \
  > /tmp/de-prod-01-values.yaml
```

What this does:

1. Creates (or patches) a `Cluster` named `de-prod-01` on the hub with a
   bootstrap slot (`spec.bootstrap.ttl=1h` by default).
2. Waits for the hub reconciler to provision a per-cluster bootstrap
   `ServiceAccount`, RBAC, and a kubeconfig `Secret` containing a short-lived
   `TokenRequest` bearer token.
3. Writes the Secret (rewritten to target the spoke install namespace) to
   `--secret-out`.
4. Writes Helm values (cluster name, hub URL, hub CA bundle, Secret name) to
   stdout.

Flags worth knowing:

| Flag | Default | Notes |
|---|---|---|
| `--hub-url` | (required) | Hub kube-apiserver URL reachable from the spoke. |
| `--ttl` | `1h` | Bootstrap slot TTL written to `Cluster.spec.bootstrap.ttl`. |
| `--ca-from` | `hub-kubeconfig` | Source for the hub CA bundle: `hub-kubeconfig`, `file`, `inline`, `none`. |
| `--namespace` | `kapro-system` | Hub namespace where the bootstrap Secret lives. |
| `--spoke-namespace` | `kapro-system` | Namespace the rendered Secret will target on the spoke. |
| `--wait-timeout` | `30s` | How long to wait for the hub to populate `status.bootstrap.issuedBootstrapKubeconfig`. |

### Bootstrap material source

By default the hub publishes bootstrap material as a Kubernetes Secret:

```yaml
spec:
  bootstrap:
    ttl: 1h
    materialSource:
      type: KubernetesSecret
```

`spec.bootstrap.materialSource.type: Vault` is a preview API contract for
platforms that want the short-lived bootstrap kubeconfig published through
Vault instead of a Kubernetes Secret. The built-in hub controller does **not**
write to Vault in this release. When a Cluster selects Vault and no external
platform automation handles it, the controller fails closed with
`Stalled=True, reason=BootstrapVaultDisabled` and does not mint a fallback
Kubernetes Secret.

```yaml
spec:
  bootstrap:
    ttl: 1h
    materialSource:
      type: Vault
      vault:
        address: https://vault.example.com
        mount: secret
        path: kapro/bootstrap/de-prod-01
        kubeconfigField: kubeconfig
```

## Step 2 — Switch kubectl context to the spoke

```bash
kubectl config use-context my-spoke-cluster
```

## Step 3 — Apply the Secret and install the chart

```bash
kubectl apply -f /tmp/de-prod-01-bootstrap-secret.yaml

helm install kapro-cluster-controller \
  https://github.com/Kapro-dev/kapro/releases/download/v0.6.0/kapro-cluster-controller-0.6.0.tgz \
  -n kapro-system --create-namespace \
  -f /tmp/de-prod-01-values.yaml
```

For source-checkout development, replace the release URL with
`charts/kapro-cluster-controller`.

Watch the agent come up:

```bash
kubectl -n kapro-system rollout status deployment/kapro-cluster-controller-kapro-cluster-controller
kubectl -n kapro-system logs -l app.kubernetes.io/name=kapro-cluster-controller -f
```

On the first boot you should see:

```
loaded existing cert from local Secret  (after first run)
CSR submitted, waiting for approver
CSR approved and signed
registered with hub                     cluster=de-prod-01
```

## Step 4 — Verify on the hub

```bash
kubectl get cluster de-prod-01 -o yaml
```

Look for:

- `status.phase: Ready`
- `status.bootstrap.used: true`
- `status.bootstrap.usedAt: <recent timestamp>`
- `status.capabilities.nodeCount: <your node count>`
- A non-empty `status.controllerVersion`

## Reachability and Ready condition

Once registered, this Cluster's `status.conditions[Ready]` and
`status.phase` are maintained by the `ClusterHeartbeatReconciler`.

Each spoke renews a hub-side `Lease` named `kapro-heartbeat-<cluster>` in the
operator namespace. The hub marks a cluster `Ready=False` and eventually
`Phase=Unreachable` when the lease is stale for the configured failure
threshold.

Operational behavior:

- stale heartbeat blocks new pull-mode work for that cluster;
- in-flight targets wait while the cluster is temporarily unreachable;
- heartbeat staleness does not directly fail a `Target`; the target
  defers until the cluster recovers or an operator takes explicit action;
- a `PromotionRun` may still fail if its own global timeout expires while
  targets are deferred;
- recovery is automatic once the spoke renews the lease again.

Common Ready reasons:

| Reason | Meaning |
|---|---|
| `HeartbeatFresh` | Lease is current and the spoke is reachable. |
| `HeartbeatStale` | Lease is stale but not yet past the failure threshold. |
| `Unreachable` | Failure threshold exceeded; pull-mode promotion targets defer instead of failing directly. |
| `Suspended` | `Cluster.spec.suspend=true`; heartbeat is intentionally ignored. |
| `PushModeNoHeartbeat` | Push-mode cluster; no spoke heartbeat is expected. |
| `NotRegistered` | The cluster has not completed bootstrap registration yet. |

## Cert rotation

Rotation is fully automatic. The spoke uses the issued client cert for
steady-state hub API calls and submits a renewal CSR at ~50% of cert lifetime
(default 1 year). No operator action is required and no chart values need to
be tuned.

If a spoke has been offline long enough that its cert has expired (>1 year by
default), re-run `kapro spoke bootstrap` to mint a fresh bootstrap kubeconfig
Secret and `kubectl apply` it; the spoke pod will pick up the new mount on
next restart and bootstrap a new cert.

## Troubleshooting

### `status.bootstrap.issuedBootstrapKubeconfig` never populates

The hub reconciler isn't running or is failing. Check operator logs:

```bash
kubectl -n kapro-system logs deployment/kapro-kapro-operator | grep -i bootstrap
```

The CLI surfaces this as: `status.bootstrap.issuedBootstrapKubeconfig not
populated within 30s` (or whatever `--wait-timeout` was set to).

If the Cluster sets `spec.bootstrap.materialSource.type: Vault`, this status
field is intentionally empty unless external Vault automation writes back a
compatible status. Check for `Stalled=True` with reason
`BootstrapVaultDisabled`; remove `materialSource` or use
`type: KubernetesSecret` to use the built-in controller path.

### Spoke pod CrashLoopBackOff with `KAPRO_CLUSTER_NAME is required`

Helm values didn't render — most often because `cluster.name` was empty.
Re-generate values with `kapro spoke bootstrap` and confirm the resulting
file contains a non-empty `cluster.name`.

### Spoke logs: `CSR not approved within 5m`

The hub approver isn't matching the CSR. Common causes:

- The bootstrap kubeconfig Secret was applied to the wrong spoke (the
  bootstrap SA is bound to a specific cluster name). Re-run
  `kapro spoke bootstrap` for **this** cluster name.
- Slot TTL expired (`spec.bootstrap.expiresAt` in the past). Re-run with a
  longer `--ttl` or recreate the Cluster.
- Slot already consumed (`status.bootstrap.used: true` with a different
  `boundCSRName`). Delete and recreate the Cluster to mint a fresh
  slot.

### Spoke logs: `tls: certificate signed by unknown authority`

The hub CA bundle baked into the chart values is wrong or missing. The
`--ca-from hub-kubeconfig` default extracts it from your local kubeconfig —
if that kubeconfig points at a private CA the spoke doesn't trust, use
`--ca-from file --ca-file /path/to/hub-ca.crt` instead. For a hub with a
publicly trusted cert, `--ca-from none` is fine.

### Heartbeat is stale but pod is running

The spoke pod is up but its per-cluster RBAC binding on the hub is missing.
Check `status.bootstrap` on the Cluster — `IssuedClusterRole` and
`IssuedClusterRoleBinding` should be populated. If they aren't, the hub
reconciler hit an error mid-provisioning; check operator logs.
