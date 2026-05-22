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

	"kapro.io/kapro/pkg/kapro/gate"
	"kapro.io/kapro/pkg/kapro/server"
)

func main() {
	opts := server.OptionsFromEnv()
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	s, err := server.New(opts)
	if err != nil {
		log.Fatal(err)
	}

	s.Gates.MustRegister("business-hours", gate.Func(func(ctx context.Context, req gate.Request) (gate.Result, error) {
		hour := time.Now().UTC().Hour()
		if hour >= 8 && hour < 18 {
			return gate.MakePassed("inside business hours"), nil
		}
		return gate.MakeInconclusive("OutsideBusinessHours", time.Now().UTC().Add(30*time.Minute)), nil
	}))

	if err := s.Run(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}
```

`server.New` wires the same defaults as the reference binary: manager options,
built-in gates, built-in actuator registrars, selected controllers, admission
webhooks, the plugin gateway, approval server, hub gateway, metrics, health
checks, and leader election. Register custom hooks after `New` returns and
before `Run`.

The stable actuator SDK import path is
`kapro.io/kapro/pkg/kapro/actuator`. The legacy `kapro.io/kapro/pkg/actuator`
package remains as a v0.2.x compatibility bridge for existing in-tree and
out-of-tree code.

Use `Options.ActuatorRegistrars` when a custom binary needs to replace or
extend the reference actuator set before controllers start:

```go
opts := server.OptionsFromEnv()
opts.ActuatorRegistrars = append(server.DefaultActuatorRegistrars(),
	server.RegisterActuator(actuator.Registration{
		Name: "push/external",
		Mode: kaprov1alpha2.DeliveryModePush,
		Capabilities: actuator.Capabilities{
			Driver:              kaprov1alpha2.BackendDriverExternal,
			Adapter:             "external",
			Runtime:             kaprov1alpha2.BackendRuntimeHub,
			Modes:               []kaprov1alpha2.DeliveryMode{kaprov1alpha2.DeliveryModePush},
			SupportsApply:       true,
			SupportsRollback:    true,
			SupportsConvergence: true,
		},
		Actuator: myActuator,
	}),
)
```

Leaving `ActuatorRegistrars` nil keeps the reference behavior. Setting it to a
non-nil slice replaces the built-in list; append to
`DefaultActuatorRegistrars()` when you want built-ins plus custom substrates.

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
boundary is required. See `docs/programmable-gates.md` for the full trust
boundary discussion.
