# checkout-direct Kapro Direct Profile Repo

This repo is a greenfield Kapro scaffold for direct Kubernetes apply.

Artifact input: the Deployment image must be pullable by the target cluster. For
local Kind testing, push a disposable image to a local registry:

```bash
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
docker pull nginx:1.27
docker tag nginx:1.27 localhost:5001/kapro/checkout-direct:0.1.0
docker push localhost:5001/kapro/checkout-direct:0.1.0
```

Apply order:

1. substrates/
2. apps/
3. clusters/
4. deliveryunits/
5. plans/
6. fleets/
7. promotions/

Apply with:

```bash
kubectl apply -f substrates/direct.yaml
kubectl wait --for=condition=Ready substrate/direct --timeout=90s
kubectl apply --recursive -f apps -f clusters -f deliveryunits -f plans -f fleets -f promotions
```

Kapro coordinates promotion. The direct profile applies the starter workload
manifests during bootstrap and updates Deployment images through the Kubernetes
API during promotion.
