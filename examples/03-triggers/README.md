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
