# Git Audit Consumer Example

This example shows how to consume Kapro CloudEvents and write a compact promotionrun
audit record to git.

The consumer is intentionally outside Kapro core:

```text
Kapro -> CloudEvents webhook -> git-audit consumer -> audit repo commit
```

This keeps the operator small while proving that promotionrun history can still live
in git for teams that need `git log`, `git diff`, and external audit review.

## Event Subscription

Configure a Kapro notification on the relevant gate policy:

```yaml
notifications:
  - type: webhook
    events:
      - kapro.promotionrun.completed
      - kapro.promotionrun.failed
    webhook:
      url: https://git-audit.example.com/events
      format: cloudevents
```

## Local Run

```bash
export AUDIT_REPO=/tmp/kapro-promotionrun-audit
go run ./examples/event-consumers/git-audit
```

The example writes YAML files under:

```text
$AUDIT_REPO/promotionruns/<promotionrun>.yaml
```

By default it only writes the file. Set `GIT_COMMIT=true` to also run
`git add` and `git commit` in `$AUDIT_REPO`:

```bash
export AUDIT_REPO=/tmp/kapro-promotionrun-audit
export GIT_COMMIT=true
go run ./examples/event-consumers/git-audit
```

It does not push. Production deployments should add credential handling, branch
protection, idempotency checks, signed commits, and a controlled push path.

## Example Output

```yaml
promotionRun: "checkout-v1-2-3"
eventType: "kapro.promotionrun.completed"
phase: "Complete"
version: "oci://registry.example.com/checkout@sha256:..."
promotionPlan: "main"
stage: "production-eu"
target: "prod-eu"
source: "/kapro/promotionruns/checkout-v1-2-3"
subject: "promotionplan/main/stage/production-eu/target/prod-eu"
eventID: "promotionrun/checkout-v1-2-3/type/kapro.promotionrun.completed/phase/Complete"
```
