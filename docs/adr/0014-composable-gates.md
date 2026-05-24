# ADR-0014: Composable gates scaffold

## Status
Accepted

Superseded in part by the v0.2.x gate algebra work: `ANY`, `NOT`,
`WEIGHTED_SUM`, `THRESHOLD`, and `DELAY` are now implemented for the
`GateExpression` preview controller. The original decision below records the
v0.1.2 scaffold and remains useful historical context.

## Context

Kapro stages already support inline gate policies for approval, soak, metrics,
verification, and template-based gates. As adoption grows, platform teams need
to reuse bundles of checks across many plans without copy-pasting large inline
gate blocks.

The full gate algebra is bigger than the `v0.1.2` public-preview window. `ANY`,
`NOT`, weighted scores, thresholds, and time delays need careful semantics
around partial failure, retries, and audit evidence.

## Decision

Introduce `GateExpression` as a Tier-B preview CRD and implement only the `ALL`
operator in `v0.1.2`.

`GateExpression` operands can be inline gate policies or references to other
`GateExpression` objects. Admission enforces exactly one operand shape,
rejects reference cycles, and rejects non-`ALL` operators with:

```text
operator X is reserved for v0.2.0; use ALL
```

The controller is installed but not enabled by default. Users opt in by adding
`gateexpression` to the controller list.

`Plan.spec.stages[].gate.expressionRef` is reserved in the schema for forward
compatibility, but Plan admission rejects it in `v0.1.2` until `Target`
reconciliation resolves and enforces referenced expressions. Enforceable gates
remain inline on Plan stages.

## Rejected alternatives

### Implement the full algebra immediately

That would make the preview surface look complete before the runtime and audit
semantics are proven. Deferring the operators keeps the CRD shape visible
without committing to behavior that has not been validated.

### Keep gate composition purely inline

Inline composition avoids another CRD, but it pushes duplication into every
Plan and makes policy review harder. A named object gives platform teams one
place to audit common gate bundles.

### Enable the controller by default

`GateExpression` is a preview controller. Keeping it Tier B follows ADR-0010:
the CRD can exist, but runtime behavior is explicit opt-in until adoption proves
the shape.

## Consequences

The API surface grows by one CRD before the full algebra is implemented. In
exchange, users can start authoring reusable gate bundles and Kapro can validate
the naming, cycle detection, and status model before `v0.2.0`.

## References

- [ADR-0010: Core and preview controller tier](0010-core-and-preview-controller-tier.md)
- [Composable gates guide](../concepts/composable-gates.md)
