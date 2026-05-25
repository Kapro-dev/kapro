# DeliveryUnit Public Preview Examples

These examples are ordered like a programming-language tutorial. Each folder
adds one concept and keeps the previous concepts visible in the YAML, so users
can diff neighboring lessons and see what changed.

| Lesson | Concept |
|---|---|
| `00-hello-world/` | Smallest useful DeliveryUnit |
| `01-source-defaults/` | Registry and source defaults |
| `02-rollout-defaults/` | Default Fleet and Plan references |
| `03-promote-one-version/` | Explicit Promotion action |
| `04-two-units/` | Multiple deployable units |
| `05-order-units/` | Wave and dependency ordering |
| `06-environment-overrides/` | Environment-specific values |
| `07-promote-per-unit-versions/` | Different versions per unit |
| `08-safe-oci-trigger/` | Suspended, dry-run OCI trigger |
| `09-trigger-guardrails/` | Cooldown, signatures, metadata, and parameters |

Apply each folder independently while learning the API:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/00-hello-world/
kubectl apply -f examples/00-deliveryunit-lessons/01-source-defaults/
kubectl apply -f examples/00-deliveryunit-lessons/02-rollout-defaults/
kubectl apply -f examples/00-deliveryunit-lessons/03-promote-one-version/
kubectl apply -f examples/00-deliveryunit-lessons/04-two-units/
kubectl apply -f examples/00-deliveryunit-lessons/05-order-units/
kubectl apply -f examples/00-deliveryunit-lessons/06-environment-overrides/
kubectl apply -f examples/00-deliveryunit-lessons/07-promote-per-unit-versions/
kubectl apply -f examples/00-deliveryunit-lessons/08-safe-oci-trigger/
kubectl apply -f examples/00-deliveryunit-lessons/09-trigger-guardrails/
```

The trigger example is intentionally suspended and dry-run by default. Change
those fields only after the registry, signature, Fleet, and Plan policy are
ready for real automatic promotion intent.

Validate the lesson set locally:

```bash
go test ./examples/00-deliveryunit-lessons
scripts/validate-yaml-json
```
