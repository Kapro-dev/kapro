# PRI Reference Examples

This chapter shows Kapro's OpenPromotions PRI reference implementation.

Start with `00-hello-world/`. It is self-contained and does not need a
Kubernetes cluster. The example validates a portable PRI Promotion document and
prints Kapro's Binding and ConformanceProfile.

Use these examples when you want to understand how a non-Kapro tool can consume
promotion records emitted by Kapro.

```bash
./examples/12-pri-reference/00-hello-world/run.sh
```

For live clusters, use the collector after a Kapro `PromotionRun` exists:

```bash
kapro pri collect --promotionrun checkout-v1-2-3 --out ./pri-records
```

The collector output is ordinary PRI YAML/JSON. It is not a new wire protocol.
