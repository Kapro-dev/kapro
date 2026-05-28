# Kapro PRI Reference Implementation

Kapro includes a reference implementation for OpenPromotions PRI v0.1.

PRI is a portable promotion contract. Kapro uses it as an interoperability
surface: other tools can validate PRI documents, consume Kapro-emitted runtime
records, or bridge those records into another promotion, audit, or evidence
system.

Kapro does not make PRI a new required control plane protocol. The reference
implementation emits ordinary PRI objects as YAML or JSON documents.

## What Kapro Implements

Kapro provides:

- a Go model and validator in `pkg/pri`;
- a collector adapter in `pkg/pri/collector`;
- `kapro pri validate` for local PRI document validation;
- `kapro pri collect` for exporting live Kapro `PromotionRun` state as PRI;
- `kapro pri profile` for Kapro's Binding and ConformanceProfile documents.

The collector is emission-mode. It reads Kapro runtime objects and writes PRI
records. It does not mutate the cluster.

## Runtime Mapping

| PRI object | Kapro source |
|---|---|
| `Promotion` | Synthesized from `runtime.kapro.io/PromotionRun` when target information is available |
| `PromotionRun` | `runtime.kapro.io/PromotionRun` |
| `TargetResult` | `runtime.kapro.io/Target.status` |
| `Evidence` | `PromotionRun.status.auditTrail` |
| `Binding` | Static Kapro reference binding |
| `ConformanceProfile` | Static Kapro reference conformance profile |

Kapro native phases are mapped into PRI's closed portable phase enums. The
native value is preserved in `status.implementationPhase` when it differs from
the portable value.

## Validate Documents

```bash
kapro pri validate examples/12-pri-reference/00-hello-world
kapro pri validate examples/12-pri-reference/00-hello-world/promotion.yaml
kapro pri validate -o json examples/12-pri-reference/00-hello-world
```

Validation rejects unknown `spec` and `status` fields so typos do not silently
become part of an integration contract.

## Collect Runtime Records

Export a live `PromotionRun` once:

```bash
kapro pri collect --promotionrun checkout-v1-2-3 --out ./pri-records
```

Run as a simple collector agent:

```bash
kapro pri collect \
  --promotionrun checkout-v1-2-3 \
  --out ./pri-records \
  --watch \
  --interval 30s
```

Print records to stdout instead:

```bash
kapro pri collect --promotionrun checkout-v1-2-3
kapro pri collect --promotionrun checkout-v1-2-3 -o json
```

## Publish Kapro's Profile

```bash
kapro pri profile
```

This prints:

- `Binding/kapro-reference`;
- `ConformanceProfile/kapro-reference`.

Use those documents when explaining how Kapro maps to PRI in an integration
review.

## How Tools Consume It

External tools do not need to run Kapro. They can consume PRI output by:

1. Validating incoming documents with the PRI schema or `pkg/pri`.
2. Reading `PromotionRun.status.phase` for portable state.
3. Reading `status.implementationPhase` for Kapro-native detail.
4. Following `targetResults[]` for target-level outcomes.
5. Linking `Evidence` records by `subjectRefs[]`.

That lets a pipeline, audit store, dashboard, policy agent, or fleet system
consume Kapro promotion records without importing Kapro's Kubernetes APIs.
