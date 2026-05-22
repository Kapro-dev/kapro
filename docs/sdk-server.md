# Kapro Server SDK

Kapro can run as an imported Go library. The stock operator image still uses
the same code path, but custom operators can construct a server, register
in-process extensions, and run their own binary.

```go
package main

import (
	"context"
	"flag"
	"log"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/kapro/server"
)

type businessHours struct{}

func (businessHours) Evaluate(ctx context.Context, req gate.Request) (gate.Result, error) {
	hour := time.Now().UTC().Hour()
	if hour >= 8 && hour < 18 {
		return gate.Result{Phase: kaprov1alpha2.GatePhasePassed, Reason: "InsideBusinessHours"}, nil
	}
	return gate.Result{Phase: kaprov1alpha2.GatePhaseInconclusive, Reason: "OutsideBusinessHours", RetryAfter: "30m"}, nil
}

func main() {
	opts := server.OptionsFromEnv()
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	s, err := server.New(opts)
	if err != nil {
		log.Fatal(err)
	}

	s.Gates.MustRegister("business-hours", businessHours{})

	if err := s.Run(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}
```

`server.New` wires the same defaults as the reference binary: manager options,
built-in gates, built-in actuators, selected controllers, admission webhooks,
the plugin gateway, approval server, hub gateway, metrics, health checks, and
leader election. Register custom hooks after `New` returns and before `Run`.

The first server SDK is intentionally a full embedded operator rather than a
minimal dependency graph. Importing `pkg/kapro/server` pulls the built-in
controllers, actuators, webhooks, and gateway packages so the reference binary
and custom binaries stay behavior-compatible.

`OptionsFromEnv` returns env-derived defaults without touching the flag
system. Bind the optional CLI flags onto your own `*flag.FlagSet` via
`(*Options).BindFlags`, then call `Parse` yourself. Cobra/pflag binaries can
copy the same fields onto a `pflag.FlagSet` and skip `BindFlags` entirely.

Use Helm's existing image override to run a custom operator image:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --set image.repository=ghcr.io/example/custom-kapro-operator \
  --set image.tag=v0.2.0-custom
```

Gate code runs in the operator process. Only compile trusted code into a custom
operator; use the gRPC plugin path when a separate security or ownership
boundary is required.
