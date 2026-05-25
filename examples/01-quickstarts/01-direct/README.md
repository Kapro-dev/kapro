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

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/01-direct/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/01-quickstarts/01-direct/apps/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
