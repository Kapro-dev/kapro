# 04 Two Units

One DeliveryUnit can describe multiple deployable units that move together.

```text
DeliveryUnit
  |-- web
  `-- worker
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/04-two-units/
```
