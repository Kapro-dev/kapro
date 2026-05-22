# Kapro Server SDK

Kapro can run as an imported Go library. The stock operator image still uses
the same code path, but custom operators can construct a server, register
in-process extensions, and run their own binary.

```go
package main

import (
	"context"
	"log"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/kapro/server"
)

func main() {
	s, err := server.New(server.OptionsFromEnv())
	if err != nil {
		log.Fatal(err)
	}

	s.Gates.MustRegister("business-hours", gate.Func(func(ctx context.Context, req gate.Request) (gate.Result, error) {
		hour := time.Now().UTC().Hour()
		if hour >= 8 && hour < 18 {
			return gate.MakePassed("inside business hours"), nil
		}
		return gate.MakePending("OutsideBusinessHours", time.Now().UTC().Add(30*time.Minute)), nil
	}))

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
