# Go SDK

Kapro ships a small Go SDK at `kapro.io/kapro/pkg/kapro` for teams that want to
author Promotions from CI systems, receive Kapro CloudEvents, or implement
custom gate evaluators without importing controller internals.

The SDK is deliberately thin:

- builders return normal `kapro.io/v1alpha2` Kubernetes objects
- subscribers consume the preview-compatible `pkg/events` CloudEvents vocabulary
- gates use a minimal `Evaluate(ctx, request)` interface

## Install

```bash
go get kapro.io/kapro@main
```

After `v0.1.2` is tagged, use the release tag instead:

```bash
go get kapro.io/kapro@v0.1.2
```

## Build a Promotion

```go
package main

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kapro "kapro.io/kapro/pkg/kapro"
)

func promote(ctx context.Context, c client.Client) error {
	promotion := kapro.NewPromotion("checkout-v123").
		ForFleet("checkout").
		AtVersion("v1.2.3").
		Build()

	return c.Create(ctx, promotion)
}
```

The builder does not hide the Kubernetes API. You can still set fields directly
on the returned object before sending it to a client.

## Build Fleet and Plan objects

```go
import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha2 "kapro.io/kapro/api/v1alpha2"
	kapro "kapro.io/kapro/pkg/kapro"
)

fleet := kapro.NewFleet("checkout").
	WithBackend("flux").
	WithCluster("dev-us", map[string]string{"env": "dev"}).
	Build()

plan := kapro.NewPlan("progressive").
	WithStage(v1alpha2.Stage{
		Name: "dev",
		Selector: metav1.LabelSelector{
			MatchLabels: map[string]string{"env": "dev"},
		},
	}).
	Build()
```

The v0.1.x SDK intentionally covers the happy path first. Full builder coverage
for every CRD field is planned after the public preview API settles.

## Receive CloudEvents

```go
sub := kapro.NewSubscriber(":8080")
sub.On(events.EventPromotionSucceeded, func(event events.Event) {
	log.Printf("promotion %s succeeded at %s", event.PromotionName, event.Version)
})

if err := sub.Run(context.Background()); err != nil {
	log.Fatal(err)
}
```

Kapro posts CloudEvents v1.0 structured JSON to the subscriber. Successful
handlers return HTTP 204. Decode errors and handler panics return a non-2xx
response so the upstream sink can retry according to its own delivery policy.

In `v0.1.x`, `On` handlers do not return errors and do not receive a request
context. If handler code calls Slack, PagerDuty, a database, or another external
system, handle that system's retry/idempotency contract inside the handler.
Multiple handlers registered for the same event type run in order; if a later
handler panics, upstream retry can replay earlier handler side effects.

## Implement a Gate

```go
type BudgetGate struct{}

func (BudgetGate) Evaluate(ctx context.Context, req kapro.GateRequest) (kapro.GateResult, error) {
	if req.Stage == "prod" {
		return kapro.GateResult{Phase: "Pending", Reason: "WaitingForBudget"}, nil
	}
	return kapro.GateResult{Phase: "Passed", Reason: "Allowed"}, nil
}
```

The SDK gate interface is a programmatic shape for custom evaluators. Runtime
registration and plugin process management stay outside this package.

## Versioning

During Kapro `v0.1.x`, `pkg/kapro` targets `kapro.io/v1alpha2`. Existing
exported names are treated as preview-compatible within the release line unless
a security or correctness fix requires a documented break. See
[ADR-0013](adr/0013-sdk-versioning-policy.md) for the policy.

## Examples

Runnable examples live in
[`examples/sdk-go`](https://github.com/Kapro-dev/kapro/tree/main/examples/sdk-go).
