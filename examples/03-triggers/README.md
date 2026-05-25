# Triggers

Standalone Trigger examples for OCI artifact watching.

```text
OCI tags -> Trigger -> Promotion intent -> PromotionRun
```

Files:

- `oci-safe-default.yaml` starts suspended for inspection.
- `oci-cosign-keyless.yaml` shows signature-aware trigger configuration.

Validate with:

```bash
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
echo "trigger artifact" > artifact.txt
oras push --plain-http localhost:5001/triggers/checkout:v1.0.0 \
  --artifact-type application/vnd.kapro.example \
  artifact.txt:text/plain
scripts/validate-yaml-json
```

The trigger manifests use placeholder repositories. Replace them with
`localhost:5001/...` for host-only testing or `kapro-registry:5000/...` for
Kind-based testing.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/03-triggers/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/03-triggers/run.sh apply
```

For OCI-backed lessons, seed a local registry artifact before applying resources that reference registry content:

```bash
examples/03-triggers/run.sh oci-prep
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.
- `oci-prep` starts or reuses a local zot registry on `localhost:5001` and pushes a small ORAS artifact.

## Cleanup

```bash
kubectl delete -f examples/03-triggers --ignore-not-found
```
