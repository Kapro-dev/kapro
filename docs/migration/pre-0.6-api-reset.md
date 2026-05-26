# Pre-0.6 API Reset

Kapro 0.6 is the first public-preview API line. It intentionally resets older
prototype manifests instead of serving conversion webhooks for unreleased
schemas.

Use this guide only if you applied Kapro manifests from an older branch or
private preview. New installs should start with [Install](../getting-started/install.md)
and [First Promotion in 10 Minutes](../getting-started/first-promotion-10min.md).

## Current API Groups

User-authored desired state uses:

```yaml
apiVersion: kapro.io/v1alpha1
```

Controller-owned runtime state uses:

```yaml
apiVersion: runtime.kapro.io/v1alpha1
```

Do not commit `runtime.kapro.io` objects to Git. They are produced by the
controller from user-authored intent.

## Main Renames

| Old prototype surface | 0.6 public-preview surface |
|---|---|
| `apiVersion: kapro.io/v1alpha2` | `apiVersion: kapro.io/v1alpha1` for desired state |
| controller-owned objects in `kapro.io/v1alpha2` | `apiVersion: runtime.kapro.io/v1alpha1`; do not commit these to Git |
| `Backend` | `Substrate` |
| `spec.delivery.backendRef` | `spec.delivery.ref` |
| `backendKind` | `substrateKind` |
| `argo-cd` driver/profile name | `argo` |
| `PromotionRun`, `Target`, `DecisionTrace` in `kapro.io` | runtime objects in `runtime.kapro.io` |
| separate gate-expression CRD | inline `Plan.spec.stages[].gate` |
| fleet drift report CRD | observe through `PromotionRun`, `Target`, and substrate evidence |

## Reset Steps

1. Export any prototype objects you still need for reference.
2. Delete old Kapro CRDs and controller deployments from the test cluster.
3. Install the 0.6 CRDs and operator.
4. Recreate desired state from the current quickstarts or generated bootstrap
   output.
5. For existing GitOps installs, run `kapro import argo` or `kapro import flux`
   and review the generated files before enabling write permissions.

There is no automatic conversion path for the prototype APIs because no
production users depend on them. The clean reset keeps the 0.6 surface small
and avoids conversion-webhook debt before the preview API settles.

For a one-off cleanup in a test cluster, delete old prototype CRDs and re-apply
the generated 0.6 manifests instead of editing stored objects in place. Runtime
objects such as `PromotionRun`, `Target`, and `DecisionTrace` are recreated by
the controller from `Promotion` intent and should not be migrated as source
manifests.
