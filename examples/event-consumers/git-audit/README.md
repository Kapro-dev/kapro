# Git Audit Consumer Example

This example shows how to consume Kapro CloudEvents and write a compact release
audit record to git.

The consumer is intentionally outside Kapro core:

```text
Kapro -> CloudEvents webhook -> git-audit consumer -> audit repo commit
```

This keeps the operator small while proving that release history can still live
in git for teams that need `git log`, `git diff`, and external audit review.

## Event Subscription

Configure a Kapro notification on the relevant gate policy:

```yaml
notifications:
  - type: webhook
    events:
      - kapro.release.completed
      - kapro.release.failed
    webhook:
      url: https://git-audit.example.com/events
      format: cloudevents
```

## Local Run

```bash
export AUDIT_REPO=/tmp/kapro-release-audit
go run ./examples/event-consumers/git-audit
```

The example writes YAML files under:

```text
$AUDIT_REPO/releases/<release>.yaml
```

By default it only writes the file. Set `GIT_COMMIT=true` to also run
`git add` and `git commit` in `$AUDIT_REPO`:

```bash
export AUDIT_REPO=/tmp/kapro-release-audit
export GIT_COMMIT=true
go run ./examples/event-consumers/git-audit
```

It does not push. Production deployments should add credential handling, branch
protection, idempotency checks, signed commits, and a controlled push path.

## Example Output

```yaml
release: "checkout-v1-2-3"
eventType: "kapro.release.completed"
phase: "Complete"
version: "oci://registry.example.com/checkout@sha256:..."
pipeline: "main"
stage: "production-eu"
target: "prod-eu"
source: "/kapro/releases/checkout-v1-2-3"
subject: "pipeline/main/stage/production-eu/target/prod-eu"
eventID: "release/checkout-v1-2-3/type/kapro.release.completed/phase/Complete"
```
