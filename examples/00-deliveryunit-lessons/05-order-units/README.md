# 05 Order Units

Use waves and dependencies when one unit should wait for another.

```text
wave 0: web
wave 1: worker dependsOn web
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/05-order-units/
```
