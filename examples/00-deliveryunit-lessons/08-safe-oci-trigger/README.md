# 08 Safe OCI Trigger

Embedded triggers derive Trigger objects. Start public examples suspended and
dry-run so detection does not automatically create Promotion intent.

```text
OCI tags -> suspended dry-run Trigger -> no Promotion write
```

Apply from the repository root:

```bash
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
echo "hello trigger" > artifact.txt
oras push --plain-http localhost:5001/tutorial/hello-world:v0.4.0 \
  --artifact-type application/vnd.kapro.example \
  artifact.txt:text/plain
kubectl apply -f examples/00-deliveryunit-lessons/08-safe-oci-trigger/
```

The manifest uses `registry.example.com` as a placeholder. For an actual local
run, replace it with `localhost:5001` for host-side commands or
`kapro-registry:5000` for a Kind cluster joined to the registry container.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/00-deliveryunit-lessons/08-safe-oci-trigger/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/00-deliveryunit-lessons/08-safe-oci-trigger/run.sh apply
```

For OCI-backed lessons, seed a local registry artifact before applying resources that reference registry content:

```bash
examples/00-deliveryunit-lessons/08-safe-oci-trigger/run.sh oci-prep
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.
- `oci-prep` starts or reuses a local zot registry on `localhost:5001` and pushes a small ORAS artifact.

## Cleanup

```bash
kubectl delete -f examples/00-deliveryunit-lessons/08-safe-oci-trigger --ignore-not-found
```
