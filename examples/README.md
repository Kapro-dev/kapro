# Examples

The examples tree is organized as indexed chapters. Start with the lowest
number that matches what you want to learn; higher numbers cover richer or more
specialized workflows.

## Learning Path

New users should start with `00-deliveryunit-lessons/`. Those folders are a
lesson-by-lesson tour: hello world, source defaults, rollout defaults,
Promotion, multiple units, ordering, overrides, per-unit versions, and safe
triggers.

After that, use `01-quickstarts/` to apply a complete working shape for your
substrate:

- `00-flux/` for the default GitOps quickstart.
- `01-direct/` for direct Kubernetes apply.
- `02-argo/` for Argo CD.
- `03-oci/` for OCI bundle pull mode.

Operators can jump to `08-monitoring/`, `10-kind-demo/`, and `11-rbac/`.
Extension authors should start with `05-plugins/`, then `06-sdk-go/`, then the
minimal custom actuator in `07-actuator-hello-world/`.

Every example directory includes a `run.sh` wrapper. The default command is a
safe local check. The README shows the lesson and the script is the executable
entrypoint for that same lesson:

```bash
./examples/run.sh
./examples/00-deliveryunit-lessons/00-hello-world/run.sh
./examples/06-sdk-go/00-promote-with-builder/run.sh test
```

Use `apply` for Kubernetes YAML examples, `run` for Go examples, and `oci-prep`
when you want the runner to start a local zot registry and push a small ORAS
artifact for OCI lessons:

```bash
./examples/00-deliveryunit-lessons/00-hello-world/run.sh apply
./examples/06-sdk-go/00-promote-with-builder/run.sh run
./examples/01-quickstarts/03-oci/run.sh oci-prep
```

CI runs the same entrypoints through `make check-examples`, which calls
`examples/run-all.sh check` and then `go test ./examples/...`. That keeps the
public READMEs, scripts, YAML, and Go examples connected.

## Local Lab

The full local cycle is the Kind demo:

```bash
scripts/kind-demo.sh up
scripts/kind-demo.sh status
scripts/kind-demo.sh approve
scripts/kind-demo.sh down
```

For examples that need a Kubernetes API, use a local Kind cluster with Kapro
installed before applying manifests. For examples that reference OCI artifacts,
use a local registry while learning:

```bash
kind create cluster --name kapro-examples
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
docker network connect kind kapro-registry || true
kubectl cluster-info --context kind-kapro-examples
```

Use `localhost:5001` from your laptop and `kapro-registry:5000` from inside the
Kind network. Replace `registry.example.com` placeholders in examples before
expecting a registry-backed example to pull real artifacts.

The OCI artifact workflow follows the ORAS quickstart pattern:

```bash
oras version
echo "hello from kapro" > artifact.txt
oras push --plain-http localhost:5001/kapro/hello-world:v0.1.0 \
  --artifact-type application/vnd.kapro.example \
  artifact.txt:text/plain
oras pull --plain-http localhost:5001/kapro/hello-world:v0.1.0
oras discover --plain-http localhost:5001/kapro/hello-world:v0.1.0
```

The ORAS project documents this flow with a local zot registry in its quickstart:
https://oras.land/docs/quickstart/

## Artifact Inputs

Not every example needs OCI.

| Path | What Must Exist |
|---|---|
| Direct apply | A container image tag that Kubernetes can pull |
| Flux GitOps | Git or Flux source objects reachable by Flux |
| Argo CD | Argo CD `Application` objects and the repo/revision they point at |
| OCI substrate | OCI bundle artifacts in a registry |
| OCI triggers | Registry tags for the Trigger to observe |
| SDK, monitoring, RBAC, archive docs | Usually no OCI artifact; follow the README for the command or manifest |

For public demos, prefer local throwaway inputs:

```bash
# image-style examples
docker pull nginx:1.27
docker tag nginx:1.27 localhost:5001/kapro/hello-world:0.1.0
docker push localhost:5001/kapro/hello-world:0.1.0

# generic OCI artifact examples
echo "hello from kapro" > artifact.txt
oras push --plain-http localhost:5001/kapro/hello-world:v0.1.0 \
  --artifact-type application/vnd.kapro.example \
  artifact.txt:text/plain
```

| Chapter | Topic |
|---|---|
| `00-deliveryunit-lessons/` | DeliveryUnit lessons from hello world to safe triggers |
| `01-quickstarts/` | Flux, direct apply, Argo CD, and OCI quickstarts |
| `02-plans/` | Reusable rollout Plan patterns |
| `03-triggers/` | Standalone Promotion trigger examples |
| `04-substrates/` | Substrate classes, existing GitOps adoption, cloud helpers, and ClusterTemplate |
| `05-plugins/` | External actuator, gate, planner, and CloudEvents examples |
| `06-sdk-go/` | Public Go SDK examples |
| `07-actuator-hello-world/` | Minimal KAI actuator implementation |
| `08-monitoring/` | Prometheus, Grafana, and kube-state-metrics assets |
| `09-archive/` | Long-term event archive sinks |
| `10-kind-demo/` | Local demo environment assets |
| `11-rbac/` | Recommended RBAC examples |

## Validate Locally

Use these checks before copying an example into docs, demos, or release notes:

```bash
make check-examples
go test ./examples/...
scripts/validate-yaml-json
python3 scripts/check-markdown-links.py examples
```

The CI suite also runs repository-wide tests and smoke coverage against these
paths.
