# DeliveryUnit Public Preview Examples

These examples are ordered like a programming-language tutorial. Start with a
small "hello world" DeliveryUnit, then add one concept per folder:

1. `00-hello-world/` introduces the smallest useful DeliveryUnit.
2. `01-source-defaults/` adds registry and source defaults.
3. `02-rollout-defaults/` adds default Fleet and Plan references.
4. `03-promote-one-version/` adds the explicit Promotion action.
5. `04-two-units/` adds a second deployable unit.
6. `05-order-units/` adds wave and dependency ordering.
7. `06-environment-overrides/` adds environment-specific values.
8. `07-promote-per-unit-versions/` promotes different versions per unit.
9. `08-safe-oci-trigger/` adds safe-by-default automation.
10. `09-trigger-guardrails/` adds trigger cooldown, signature, metadata, and parameters.

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
