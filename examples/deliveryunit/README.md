# DeliveryUnit Public Preview Examples

These examples are ordered like a programming-language tutorial. Start with a
small "hello world" DeliveryUnit, then add exactly one or two concepts in each
folder:

1. `00-hello-world/` introduces the smallest useful DeliveryUnit.
2. `01-promote-hello-world/` adds the explicit Promotion action.
3. `02-add-rollout-defaults/` adds default Fleet and Plan references.
4. `03-two-units/` adds a second deployable unit and ordering.
5. `04-environment-overrides/` adds environment-specific values.
6. `05-safe-oci-trigger/` adds safe-by-default automation.

Apply each folder independently while learning the API:

```bash
kubectl apply -f examples/deliveryunit/00-hello-world/
kubectl apply -f examples/deliveryunit/01-promote-hello-world/
kubectl apply -f examples/deliveryunit/02-add-rollout-defaults/
kubectl apply -f examples/deliveryunit/03-two-units/
kubectl apply -f examples/deliveryunit/04-environment-overrides/
kubectl apply -f examples/deliveryunit/05-safe-oci-trigger/
```

The trigger example is intentionally suspended and dry-run by default. Change
those fields only after the registry, signature, Fleet, and Plan policy are
ready for real automatic promotion intent.
