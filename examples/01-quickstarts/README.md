# Quickstarts

Complete substrate-specific examples. Start with Flux unless you already know
which execution path you want.

```text
DeliveryUnit + Plan + Fleet + Promotion
              |
              v
        chosen substrate
```

| Folder | Use When |
|---|---|
| `00-flux/` | Default GitOps quickstart |
| `01-direct/` | Direct Kubernetes apply without GitOps |
| `02-argo/` | Argo CD owns application sync |
| `03-oci/` | Spokes pull OCI bundle artifacts |

## Artifact Inputs

Each quickstart needs a different kind of input:

| Folder | Artifact/Input |
|---|---|
| `00-flux/` | Flux source or Git state for the referenced workload |
| `01-direct/` | Container image tag in the generated Deployment |
| `02-argo/` | Argo CD Applications and their Git revisions |
| `03-oci/` | OCI bundle artifact in a registry |

Use a local registry for image or OCI examples while learning:

```bash
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
```

Validate quickstart YAML with:

```bash
scripts/validate-yaml-json
go test ./internal/lint
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/01-quickstarts/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/01-quickstarts/00-flux/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
