# 03 Promote One Version

Promotion is the explicit action. This rolls one version of the DeliveryUnit
through the named Fleet.

```text
DeliveryUnit + Promotion(version) -> PromotionRun
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/03-promote-one-version/
```
