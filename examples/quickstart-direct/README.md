# checkout-direct Kapro Direct Profile Repo

This repo is a greenfield Kapro scaffold for direct Kubernetes apply.

Apply order:

1. substrates/
2. apps/
3. clusters/
4. plans/
5. fleets/
6. promotions/

Apply with:

```bash
kubectl apply -f substrates/direct.yaml
kubectl wait --for=condition=Ready substrate/direct --timeout=90s
kubectl apply --recursive -f apps -f clusters -f plans -f fleets -f promotions
```

Kapro coordinates promotion. The direct profile applies the starter workload
manifests during bootstrap and updates Deployment images through the Kubernetes
API during promotion.
