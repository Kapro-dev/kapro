# v1alpha1 to v1alpha2 Migration

Kapro `v0.1.x` uses `kapro.io/v1alpha2` CRDs. The move from the prototype
`kapro.io/v1alpha1` API is a clean pre-stable break: the shipped CRDs do not
serve `v1alpha1`, and there is no automatic conversion for stored legacy
objects.

Use this guide only if you applied prototype manifests from an older branch or
pre-release snapshot. New installs should start with [Install](install.md) and
[First Promotion in 10 Minutes](first-promotion-10min.md).

## What Changed

All Kapro custom resources moved from:

```yaml
apiVersion: kapro.io/v1alpha1
```

to:

```yaml
apiVersion: kapro.io/v1alpha2
```

The public object names were also shortened.

| v1alpha1 kind | v1alpha2 kind | v1alpha1 plural | v1alpha2 plural |
|---|---|---|---|
| `Kapro` | `Fleet` | `kaproes` | `fleets` |
| `FleetCluster` | `Cluster` | `fleetclusters` | `clusters` |
| `FleetClusterTemplate` | `ClusterTemplate` | `fleetclustertemplates` | `clustertemplates` |
| `AgentPolicy` | `Policy` | `agentpolicies` | `policies` |
| `PromotionSource` | `Source` | `promotionsources` | `sources` |
| `PromotionTrigger` | `Trigger` | `promotiontriggers` | `triggers` |
| `PromotionPlan` | `Plan` | `promotionplans` | `plans` |
| `PromotionTarget` | `Target` | `promotiontargets` | `targets` |
| `BackendProfile` | `Backend` | `backendprofiles` | `backends` |
| `PluginRegistration` | `Plugin` | `pluginregistrations` | `plugins` |
| `PromotionUnit` | `Unit` | n/a | n/a |

`Promotion`, `PromotionRun`, and `Approval` kept their kind names, but their
`apiVersion` is now `kapro.io/v1alpha2`.

## Field Renames

These are the user-facing field changes most likely to affect existing YAML.

| Old field | New field |
|---|---|
| `spec.kaproRef` | `spec.fleetRef` |
| `spec.promotionPlan` on `Kapro` | `spec.plan` on `Fleet` |
| `spec.promotionPlans[]` | `spec.plans[]` |
| `spec.promotionPlans[].promotionPlanRef` | `spec.plans[].planRef` |
| `spec.promotionPlans[].promotionPlan` | `spec.plans[].plan` |
| `PromotionTrigger.spec.promotionTemplate.kaproRef` | `Trigger.spec.promotionTemplate.fleetRef` |
| `PromotionTrigger.spec.promotionTemplate.promotionPlans[]` | `Trigger.spec.promotionTemplate.plans[]` |
| `PromotionTrigger.spec.promotionTemplate.promotionPlans[].promotionPlan` | `Trigger.spec.promotionTemplate.plans[].plan` |
| `PromotionRun.status.promotionPlanProgress` | `PromotionRun.status.planProgress` |
| `PromotionRun.status.promotionPlanProgress[].promotionPlan` | `PromotionRun.status.planProgress[].plan` |
| `PromotionTarget.spec.promotionRunRef` | `Target.spec.runRef` |
| `PromotionTarget.spec.promotionPlanRef` | `Target.spec.planRef` |
| `PromotionTarget.spec.promotionPlan` | `Target.spec.plan` |
| `PromotionRun.status.auditTrail[].promotionRunDerivedFrom` | `PromotionRun.status.auditTrail[].runDerivedFrom` |
| CloudEvents `data.kaproRef` | CloudEvents `data.fleetRef` |

`Target` is the authoritative per-target runtime object. Do not read old inline
`PromotionRun.status.targets` data from automation; use child `Target` objects
and `PromotionRun.status.summary` instead.

## Controller Keys

If you configured Helm `controllers:` or `KAPRO_CONTROLLERS`, switch to the
canonical controller keys below. The old keys are accepted as compatibility
aliases, but new values should use the canonical names.

| Old key | Canonical key |
|---|---|
| `kapro` | `fleet` |
| `promotion-target` | `target` |
| `fleetcluster-heartbeat` | `cluster` |
| `backend-profile` | `backend` |
| `plugin-registration` | `plugin` |
| `promotion-trigger` | `trigger` |
| `fleetcluster-bootstrap` | `cluster-bootstrap` |
| `fleetcluster-template` | `clustertemplate` |

Current canonical keys are `fleet`, `plan`, `promotion`, `promotionrun`,
`target`, `cluster`, `approval`, `backend`, `plugin`, `trigger`,
`cluster-bootstrap`, and `clustertemplate`.

## Clean-Break Upgrade

First confirm that the installed CRDs still serve the legacy version. Do not run
the destructive cleanup after installing the `v1alpha2` chart:

```bash
for crd in $(kubectl get crd -o name | grep '[.]kapro[.]io$'); do
  kubectl get "${crd}" \
    -o jsonpath='{.metadata.name}{"\t"}{range .spec.versions[*]}{.name}{" "}{end}{"\n"}'
done
```

Back up old objects before deleting anything. The loop skips resources that were
not installed in your prototype cluster and writes one file per resource:

```bash
legacy_resources=(
  kaproes
  fleetclusters
  fleetclustertemplates
  agentpolicies
  promotionsources
  promotiontriggers
  promotionplans
  promotiontargets
  backendprofiles
  pluginregistrations
  promotions
  promotionruns
  approvals
)

mkdir -p kapro-v1alpha1-backup
for resource in "${legacy_resources[@]}"; do
  if kubectl api-resources --api-group=kapro.io --api-version=kapro.io/v1alpha1 -o name | sed 's/[.].*$//' | grep -qx "${resource}"; then
    kubectl get "${resource}" -o yaml > "kapro-v1alpha1-backup/${resource}.yaml"
  fi
done
```

Delete legacy objects while the old CRDs still exist:

```bash
for resource in "${legacy_resources[@]}"; do
  if kubectl api-resources --api-group=kapro.io --api-version=kapro.io/v1alpha1 -o name | sed 's/[.].*$//' | grep -qx "${resource}"; then
    kubectl delete "${resource}" --all --ignore-not-found
  fi
done
```

Then delete the old prototype CRDs:

```bash
kubectl delete crd \
  kaproes.kapro.io \
  fleetclusters.kapro.io \
  fleetclustertemplates.kapro.io \
  agentpolicies.kapro.io \
  promotionsources.kapro.io \
  promotiontriggers.kapro.io \
  promotionplans.kapro.io \
  promotiontargets.kapro.io \
  backendprofiles.kapro.io \
  pluginregistrations.kapro.io \
  promotions.kapro.io \
  promotionruns.kapro.io \
  approvals.kapro.io \
  --ignore-not-found
```

Install the current chart and CRDs:

```bash
KAPRO_VERSION=0.1.0
KAPRO_CHART="https://github.com/Kapro-dev/kapro/releases/download/v${KAPRO_VERSION}/kapro-operator-${KAPRO_VERSION}.tgz"

helm upgrade --install kapro \
  "${KAPRO_CHART}" \
  --namespace kapro-system \
  --create-namespace
```

Re-author manifests with the new kinds and fields. These substitutions are only
a starting point; review the result before applying it:

```bash
perl -pi -e 's#apiVersion: kapro.io/v1alpha1#apiVersion: kapro.io/v1alpha2#g' *.yaml
perl -pi -e 's#kind: Kapro#kind: Fleet#g' *.yaml
perl -pi -e 's#kind: FleetCluster#kind: Cluster#g' *.yaml
perl -pi -e 's#kind: FleetClusterTemplate#kind: ClusterTemplate#g' *.yaml
perl -pi -e 's#kind: AgentPolicy#kind: Policy#g' *.yaml
perl -pi -e 's#kind: PromotionSource#kind: Source#g' *.yaml
perl -pi -e 's#kind: PromotionTrigger#kind: Trigger#g' *.yaml
perl -pi -e 's#kind: PromotionPlan#kind: Plan#g' *.yaml
perl -pi -e 's#kind: PromotionTarget#kind: Target#g' *.yaml
perl -pi -e 's#kind: BackendProfile#kind: Backend#g' *.yaml
perl -pi -e 's#kind: PluginRegistration#kind: Plugin#g' *.yaml
perl -pi -e 's#kaproRef:#fleetRef:#g' *.yaml
perl -pi -e 's#promotionPlans:#plans:#g' *.yaml
perl -pi -e 's#promotionPlanRef:#planRef:#g' *.yaml
perl -pi -e 's#promotionPlan:#plan:#g' *.yaml
perl -pi -e 's#promotionRunRef:#runRef:#g' *.yaml
```

Validate and apply:

```bash
kapro lint --strict *.yaml
kubectl apply -f .
kapro doctor
kubectl get fleets,clusters,plans,promotions,promotionruns,targets
```

## Notes

- ADR-0011 added conversion webhook plumbing for future served-version
  transitions, but it does not provide automatic `v1alpha1` to `v1alpha2`
  conversion.
- Argo CD API references such as `argoproj.io/v1alpha1` are unrelated and
  should not be rewritten.
- Plugin protocol packages under `spec/kai/v1alpha1`, `spec/kgi/v1alpha1`,
  and `spec/kpi/v1alpha1` are separate extension contracts. They did not move
  with the Kapro CRD API.
- Discovery map `schemaVersion: kapro.io/git-adoption/v1alpha1` is a separate
  brownfield import file format, not a Kapro CRD `apiVersion`.
