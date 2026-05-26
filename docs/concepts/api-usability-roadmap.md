# API Usability Roadmap

Kapro's public API should be easy for Kubernetes users to read without learning
a separate platform vocabulary. The rule for the next cleanup train is:

```text
If Kubernetes, Argo CD, Flux, Tekton, Prometheus, or PDB already has a familiar
shape, Kapro should reuse that shape and add only the promotion-specific part.
```

## Public Language

Do not use `greenfield` or `brownfield` in first-use docs, CLI help, examples,
or CRD field names. Those words are useful in internal planning, but they make
Kapro sound like a migration framework instead of a Kubernetes promotion
control plane.

Use these public terms instead:

| Avoid | Use | Why |
| --- | --- | --- |
| greenfield | new promotion repo | Describes the action the user is taking. |
| brownfield | existing GitOps repo | Names the thing the user already has. |
| legacy estate | existing Argo CD or Flux install | Avoids judgmental language. |
| take over | adopt selected fields | Makes the write boundary explicit. |
| sync mode | delivery mode or management policy | Avoids mixing push/pull with Observe/Adopt. |

The CLI language should stay simple:

| User job | Command shape |
| --- | --- |
| Create a new promotion repo | `kapro create direct|argo|flux|oci` |
| Connect an existing GitOps repo | `kapro import argo|flux` |
| Inspect without writing | `kapro discover argo|flux` |
| Explain a blocked rollout | `kapro explain <promotionrun>` |

## CNCF Schema Alignment

This audit compares Kapro's public YAML against Kubernetes API conventions,
Gateway API, Flux, Argo CD, Tekton, Kueue, Cluster API, and Crossplane. The
goal is not to copy any one project; it is to make a Kapro manifest feel like a
Kubernetes manifest a platform engineer already knows how to read.

| Ecosystem pattern | Kapro rule |
| --- | --- |
| Kubernetes object references use a `*Ref`/`*Refs` field whose value is an object with at least `name`; broader references add `namespace`, `apiVersion`, `kind`, or resolver metadata. | Do not introduce new scalar `*Ref` fields. Either use an object reference (`pipelineRef.name`, `configRef.name`) or use a local domain noun (`unit`, `fleet`, `plan`). |
| Gateway API separates class selection from implementation ownership through class resources and controller names. | Keep `SubstrateClass.spec.controllerName`, `Substrate.spec.classRef.name`, and typed `configRef`; delete the legacy `spec.substrate.kind/actuator` duplication in v0.6.2. |
| Flux, Tekton, Argo CD, and Crossplane keep integration-specific settings in source/pipeline/provider config objects instead of adding tool-specific fields to every workload. | Keep `Promotion` substrate-neutral. Tekton, webhook, Argo, Flux, and OCI settings live under `SubstrateClass` plus typed config CRDs. |
| Kueue uses queue objects and selectors/cohorts without forcing every workload to carry a queue reference. | `PromotionQueue` should attach with selectors first; explicit `queueRef` can come later if real users need override-by-object. |
| Kubernetes, Gateway API, Kueue, and NetworkPolicy make selectors full `LabelSelector` objects when users author policy. | New Kapro policy selectors should be `matchLabels`/`matchExpressions`, not bare `map[string]string`, except for documented legacy shorthands. |
| Kubernetes and Flux use `spec.suspend`, while existing Kapro promotion objects mostly use `spec.suspended`. | Pick one spelling inside Kapro. Because `Promotion`, `Fleet`, `DeliveryUnit`, `Policy`, and triggers already use `suspended`, v0.6.2 renames the remaining public `suspend` fields to `suspended` and stops adding new `suspend` fields. |
| Tekton and Argo CD use named parameter lists for user-authored bindings; StorageClass-style `parameters` maps are opaque extension bags. | Use named lists for field projections and user-visible mapping. Use `parameters` maps only when the selected substrate/plugin owns the schema. |
| Kubernetes status uses `conditions`, `observedGeneration`, and bounded counts rather than spec echo fields that look authoritative. | Status may report observed refs and capability summaries, but stale copies of user spec must be removed or clearly marked as observations. |

## Naming Rectification

These are the deliberate `v0.6.2` public-preview breaking changes. They pull
the major `v0.7.x` naming cleanup into the public relaunch so early adopters
see one clean YAML contract instead of a deprecated alias layer.

| Old shape | `v0.6.2` public shape | Reason |
| --- | --- | --- |
| `Substrate.spec.substrate.kind` | Delete; use `spec.classRef.name` | `classRef` already selects the capability class. |
| `Substrate.spec.substrate.actuator` | Delete; use `spec.classRef.name` and controller-owned class status | Avoids encoding the same substrate twice. |
| `Cluster.spec.delivery` | `Cluster.spec.delivery` | The field configures delivery behavior, not the `Substrate` object itself. |
| `Cluster.spec.suspend` | `Cluster.spec.suspended` | Matches `Fleet`, `Promotion`, `DeliveryUnit`, and policy state naming. |
| `ClusterTemplate.spec.suspend` and `Source.spec.units[].suspend` | `suspended` | Removes the last public spelling split inside Kapro. |
| `Substrate.spec.discovery.enabled` | `Substrate.spec.discovery.suspended` or omit the discovery block | Avoids opposite boolean polarity. |
| Promotion, PromotionRun, and Trigger template bare string `deliveryUnitRef`, `fleetRef`, and `planRef` fields | `unit`, `fleet`, and `plan` | These are fixed Kapro action operands; reserve `*Ref` for object-shaped references. |
| DeliveryUnit defaults and embedded trigger overrides: `defaultFleetRef`, `defaultPlanRef`, `triggers[].fleetRef`, `triggers[].planRef` | `defaultFleet`, `defaultPlan`, `triggers[].fleet`, `triggers[].plan` | Keeps the authoring object consistent with Promotion. |
| Bare string `sourceRef` fields | Delete from first-use authoring; use `source` for local shorthand or `sourceRef.name` for retained object references | Reserve `*Ref` for typed object references. |
| Top-level scalar `substrateRef` fields | Domain noun such as `substrate`, or object refs when a broader reference is needed | `spec.delivery.ref` stays compact because the parent already says "delivery"; top-level references should not add scalar `*Ref` fields. |
| Scalar `pluginRef` and `secretRef` fields | `pluginRef.name` and `secretRef.name`/typed Secret refs | Plugins and Secrets are Kubernetes objects, so users expect object-reference shape. |
| Legacy map selectors on user-authored policy fields | Full `selector.matchLabels`/`matchExpressions` | Matches Kubernetes policy, Gateway, and Kueue shapes. |
| `Promotion.spec.version` plus `spec.versions` | `defaultVersion` plus `versionOverrides`, or one versions map | Avoids singular/plural collision. |
| `MetricGate` list item without `name` | Required `name` field | Avoids positional list semantics and makes status/event references stable. |
| Status fields that echo spec | Observed fields with freshness semantics, or remove | Avoids stale cache paths that look authoritative. |
| `PromotionRun` kubectl ergonomics | Keep short name and print columns | Already satisfied in current code; keep it in the release checklist. |

### Spec And Status Ownership

Kubernetes users expect `spec` to be durable desired state and `status` to be
controller observation. Kapro should keep that contract sharp in v0.7, even for
generated runtime objects.

| Current shape | Risk | `v0.7.x` target |
| --- | --- | --- |
| `Cluster.spec.desiredVersion`, `desiredVersions`, and `desiredAppKey` are written by Kapro controllers. | The `Cluster` object mixes platform-authored inventory with controller-authored rollout intent. That makes ownership, RBAC, and drift hard to reason about. | Move rollout intent to `PromotionRun`/`Target`, a dedicated generated delivery-intent object, or a clearly controller-owned runtime object. Keep `Cluster.spec` for platform-authored inventory and delivery defaults. |
| `Substrate.status.className`, `configRef`, `substrate`, and `execution` echo spec fields. | Users can mistake stale status copies for authoritative configuration. | Keep only observed/capability fields with freshness semantics, such as accepted class generation, probed capability set, and endpoint readiness. |
| `Fleet.status.version` echoes the currently promoted revision. | It hides multi-artifact promotion state and can conflict with per-run truth. | Prefer counts, conditions, and links to active/latest `PromotionRun`; keep per-artifact detail on runtime objects. |
| Controller-derived `Source` and `Trigger` objects have user-looking specs. | Users may edit derived objects and bypass `DeliveryUnit` as the authoring root. | Keep owner references, managed labels, admission warnings, and docs that make `DeliveryUnit` the authoring surface. |

## Roadmap Proposal

| Work item | User problem | Kubernetes-shaped proposal | Ship bar |
| --- | --- | --- | --- |
| Controller metrics | Operators need to know whether Kapro is healthy before adding more CRDs. | Expose conventional controller-runtime and Kapro domain metrics with bounded labels. | Dashboards, alerts, tests for metric registration, and documented PromQL examples. |
| PromotionDisruptionBudget | Teams need protection from voluntary promotion interruption during queue drains, supersede actions, or controller maintenance. | Mirror `PodDisruptionBudget`: `selector`, exactly one of `minAvailable` or `maxUnavailable`, status counts, and conditions. | A user who knows PDB can read the object without learning a new model. |
| PromotionQueue | Shared Kapro hubs need fair admission so one team or app cannot consume all promotion slots. | Optional queue CRD with `parentRef`, `selector`, `fairShare.weight`, and `limits.maxActive`; use KAI-style hierarchical fair-share internally, but keep the public fields small. | No change to hello world, no required `queueRef`, deterministic admission tests, and starvation tests. |
| Discriminator naming audit | Mixed `kind`, `type`, `mode`, and `substrateKind` fields make extension manifests harder to scan. | Use Kubernetes-style object `kind` only for API objects or native resource kinds, `type` for union branches such as trigger sources, and `mode` for execution topology. | No v0.6.2 churn; v0.7.x issue with field-by-field proposal before adding new discriminators. |
| Tekton substrate | Pipeline teams want Kapro to trigger an existing deploy pipeline instead of embedding delivery logic. | Reuse `SubstrateClass` plus a typed `TektonPipelineConfig` that maps PromotionRun fields into Tekton `PipelineRun` params/workspaces. | First BYOD pipeline proof with cancellation, retries, evidence links, and conformance coverage. |
| Language-neutral substrate plugin bridge | Platform teams want substrate implementations to run outside the Kapro process without losing the `SubstrateClass`/typed-config model. | Add a KSI gRPC/protobuf transport, or an explicit KSI-to-KAI bridge, registered through `Plugin` and keyed by `SubstrateClass.spec.controllerName`. | External authors can pass conformance from a live gRPC endpoint, and `SubstrateClass.status` reports capabilities from the endpoint. |
| Webhook substrate | Platform teams need to call an internal deployment API without writing a Go plugin. | Reuse `SubstrateClass` plus a typed HTTP webhook config with endpoint Secret refs, timeout, retry policy, and status mapping. | Ship only after Tekton and the gRPC substrate bridge prove the shared evidence model. |

Recommended order: metrics first, disruption budget second, queue third,
discriminator audit before new extension APIs, Tekton fourth,
language-neutral substrate plugin bridge fifth, webhook sixth.
This makes the train safer: operators get visibility before new scheduling
behavior, and the generic webhook shape is not invented before concrete
pipeline and gRPC plugin paths prove the evidence contract.

## Field Audit

### PromotionQueue

`PromotionQueue` should be optional admission control, not a required object in
normal manifests. The scheduler can use a KAI-style hierarchical fair-share
algorithm internally, but it should not expose scheduler math as user-authored
fields in the first version.

The v0.7 target common path should be:

```yaml
apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: checkout-v1-2-3
spec:
  unit: checkout
  fleet: checkout-prod
  plan: progressive
  version: v1.2.3
```

Use selectors so platform teams can attach queue policy without making every
application author add a queue reference:

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionQueue
metadata:
  name: checkout
spec:
  parentRef:
    name: platform
  selector:
    matchLabels:
      team: checkout
  fairShare:
    weight: 2
  limits:
    maxActive: 5
```

Field guidance:

| Field | Keep? | Reason |
| --- | --- | --- |
| `spec.parentRef.name` | Yes | Familiar hierarchy shape from Kubernetes APIs such as Gateway. |
| `spec.selector` | Yes | Lets teams use labels instead of editing every Promotion. |
| `spec.fairShare.weight` | Yes | Small user-facing expression of hierarchical fair-share. |
| `spec.limits.maxActive` | Yes | Directly answers "how many promotions can run now?" |
| `spec.queueRef` on `Promotion` | Maybe later | Useful for explicit opt-in, but selectors should handle the first version. |
| Internal scheduler math fields | No | Keep shares, deficits, and scores in status/events, not user spec. |

Status should use ordinary Kubernetes condition names such as `Ready`,
`Admitting`, and `Starved`, plus counts for queued, active, admitted, and
blocked promotions.

### PromotionDisruptionBudget

This is the strongest name in the roadmap because it is a direct PDB analog.
Keep it if the behavior mirrors PDB closely; rename it if the behavior drifts.

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionDisruptionBudget
metadata:
  name: checkout-prod
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: checkout
      kapro.io/stage: prod
  maxUnavailable: 1
```

Field guidance:

| Field | Keep? | Reason |
| --- | --- | --- |
| `spec.selector` | Yes | Same mental model as PDB and NetworkPolicy. |
| `spec.minAvailable` | Yes | Familiar safety expression for critical apps. |
| `spec.maxUnavailable` | Yes | Familiar rollout budget expression. |
| Both budget fields at once | No | Match PDB validation: exactly one budget field. |
| `spec.mode` | No | Too vague; describe voluntary disruption behavior in docs and status. |

The docs must say what counts as voluntary disruption: controller-initiated
cancel, supersede, queue drain, and maintenance interruption. A substrate
failure, failed gate, or workload crash is not disruption-budget protected.

### Tekton Substrate

Tekton should prove the first pipeline substrate without adding Tekton-specific
fields to `Promotion`.

```yaml
apiVersion: kapro.io/v1alpha1
kind: Substrate
metadata:
  name: tekton-deploy
spec:
  classRef:
    name: tekton
  configRef:
    apiVersion: tekton.substrate.kapro.io/v1alpha1
    kind: TektonPipelineConfig
    name: deploy
  execution:
    mode: hub-push
```

```yaml
apiVersion: tekton.substrate.kapro.io/v1alpha1
kind: TektonPipelineConfig
metadata:
  name: deploy
spec:
  pipelineRef:
    name: deploy-app
  params:
    - name: version
      valueFrom:
        promotionFieldRef:
          fieldPath: spec.version
    - name: target
      valueFrom:
        targetFieldRef:
          fieldPath: metadata.name
  serviceAccountName: pipeline-deployer
  timeout: 30m
```

Field guidance:

| Field | Keep? | Reason |
| --- | --- | --- |
| `pipelineRef` | Yes | Tekton-native reference. |
| `params` | Yes | Tekton-native parameter mapping. |
| `valueFrom.promotionFieldRef` | Yes | Kubernetes-style projection without templating strings everywhere. |
| `serviceAccountName` | Yes | Kubernetes-native execution identity. |
| `timeout` | Yes | Common Kubernetes duration field. |
| `Promotion.spec.tekton` | No | Keeps Promotion substrate-neutral. |

The controller should write evidence back to `PromotionRun`/`Target`: Tekton
namespace, PipelineRun name, URL if available, start/completion timestamps, and
terminal condition.

### Language-Neutral Substrate Plugin Bridge

Kapro already has language-neutral gRPC plugin contracts for actuators (`KAI`),
gates (`KGI`), and planners (`KPI`) through the `Plugin` CRD. KSI, the Kapro
Substrate Interface, is still a Go SDK contract in `v0.6.x`. That is acceptable
for the public preview, but it should not be the final extensibility story.

`v0.7.x` should close this gap before making webhook the main custom substrate
path:

```text
SubstrateClass(controllerName)
  -> Plugin endpoint
    -> KSI gRPC/protobuf service, or a documented KSI-to-KAI bridge
      -> external delivery implementation
```

Field and contract guidance:

| Decision | Guidance | Reason |
| --- | --- | --- |
| Transport | Prefer gRPC/protobuf for reusable external substrates. | Versioned protobuf contracts are easier to validate and evolve than ad hoc HTTP payloads. |
| Registration | Reuse `Plugin` or add a narrow substrate plugin type; do not add another endpoint CRD unless lifecycle/RBAC differs. | Keeps the extension model small. |
| Binding | Key runtime loading from `SubstrateClass.spec.controllerName`. | Matches the Kubernetes class-controller pattern already used by Gateway-style APIs. |
| Config | Keep substrate-specific settings in typed config CRDs, not plugin parameters. | Preserves the `SubstrateClass`/`configRef` architecture. |
| Status | Report endpoint capabilities into `SubstrateClass.status`. | Users should see the class capability truth through Kubernetes status. |
| Conformance | Add a live-endpoint conformance command like `kapro-conformance substrate --endpoint ...`. | External authors need the same confidence KAI/KGI/KPI plugin authors have today. |

The bridge should preserve Kapro's ownership boundary: the external substrate
does substrate-specific mutation and observation, while Kapro owns
`PromotionRun`, `Target`, retries, ordering, gates, and final status.

### Webhook Substrate

The webhook substrate should be the custom API escape hatch. It should not be
the first substrate users see, and it should not become an unbounded scripting
engine. Prefer the language-neutral gRPC substrate bridge for reusable
integrations; use webhook when the platform already exposes a narrow internal
HTTP deploy API and the team does not need a reusable plugin SDK.

```yaml
apiVersion: webhook.substrate.kapro.io/v1alpha1
kind: WebhookSubstrateConfig
metadata:
  name: internal-deploy-api
spec:
  endpointRef:
    name: deploy-api
  method: POST
  timeout: 30s
  retryPolicy:
    attempts: 3
    backoff: 10s
```

Field guidance:

| Field | Keep? | Reason |
| --- | --- | --- |
| `endpointRef` | Yes | Keeps URL and auth in Secret/config material instead of plain spec text. |
| `method` | Yes | Small enum: `POST` first, add others only with use cases. |
| `timeout` | Yes | Bounded external calls. |
| `retryPolicy` | Yes | Explicit retries instead of hidden controller behavior. |
| Arbitrary request templates | Later | Easy to misuse; start with structured PromotionRun and Target payloads. |
| Raw secrets in spec | No | Keep credentials out of CRD spec. |

### Controller Metrics

Metrics should make Kapro easier to operate without requiring a new object.
Start with low-cardinality labels only:

```text
kapro_reconcile_total{controller,result}
kapro_reconcile_duration_seconds{controller}
kapro_promotionrun_active_total{stage}
kapro_promotionrun_terminal_total{result,reason}
kapro_queue_depth{queue}
kapro_queue_admitted_total{queue,result}
kapro_substrate_apply_total{class,result}
kapro_substrate_observe_total{class,result}
```

Do not put app names, Promotion names, PromotionRun UIDs, target cluster names,
or arbitrary labels on high-volume metrics. Put those in events, status,
DecisionTrace, or logs.

## API Review Checklist

Every `v0.7.x` field should pass this checklist before it becomes public:

- The default hello-world path does not need the field.
- The field name matches Kubernetes conventions: `selector`, `matchLabels`,
  `parentRef`, `classRef`, `configRef`, `timeout`, `retryPolicy`, `conditions`,
  and `observedGeneration`.
- `*Ref` and `*Refs` fields are object-shaped. Scalar local-name fields do not
  carry the `Ref` suffix.
- The field has one owner and one meaning.
- Controllers write status for observations. They do not mutate user-authored
  spec fields unless the whole object is explicitly controller-owned.
- Open maps are avoided unless the data is genuinely extension-owned.
- Secrets are referenced, not embedded.
- Status exposes conditions and counts instead of asking users to infer from
  logs.
- The field appears in at least one runnable example and one validation test.
- Docs explain when not to use the feature.
- External extension APIs prefer versioned gRPC/protobuf plus conformance;
  webhook remains the custom API escape hatch, not the default plugin model.

## Product Decision

For `v0.7.x`, keep the public promise narrow:

```text
Kapro remains a Kubernetes-native promotion control plane. v0.7 adds operator
visibility, optional fairness, optional disruption safety, and two familiar
integration paths for teams that already use Tekton, gRPC plugins, or internal
deployment APIs.
```

That positioning avoids the worst failure mode: making Kapro sound like a new
deployment platform when the best user experience is to let teams keep the
systems they already trust.
