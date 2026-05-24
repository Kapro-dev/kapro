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

Use `server.NewBare` when you want a smaller embedded operator. It creates only
the controller-runtime manager, scheme, health checks, and empty registries; it
does not load the approval secret, register built-in gates, start controllers,
or add HTTP gateway servers until you call registrars:

```go
opts := server.OptionsFromEnv()
s, err := server.NewBare(opts)
if err != nil {
	log.Fatal(err)
}
if err := server.RegisterActuators(context.Background(), s); err != nil {
	log.Fatal(err)
}
if err := server.RegisterAdapters(context.Background(), s); err != nil {
	log.Fatal(err)
}
if err := server.RegisterGates(context.Background(), s); err != nil {
	log.Fatal(err)
}
if err := server.RegisterControllers(context.Background(), s); err != nil {
	log.Fatal(err)
}
```

`server.DefaultRegistrars()` is the reference order used by `server.New`:
actuators, adapters, gates, plugin gateway, admission webhooks, controllers,
approval server, and hub gateway. Call individual registrars when an embedded
binary intentionally omits a subsystem. Register custom predicates after
`RegisterGates` so the built-in gate set is present before your additions.
When using `NewBare` with `RegisterAdmission` in local dev, set
`Options.WebhookCertDir` or `KAPRO_WEBHOOK_CERT_DIR`; only `server.New`
auto-generates dev webhook certificates for the full reference path.

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
		Mode: kaprov1alpha1.DeliveryModePush,
		Capabilities: actuator.Capabilities{
			Driver:              kaprov1alpha1.SubstrateDriverExternal,
			Adapter:             "external",
			Runtime:             kaprov1alpha1.SubstrateRuntimeHub,
			Modes:               []kaprov1alpha1.DeliveryMode{kaprov1alpha1.DeliveryModePush},
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
and custom binaries stay behavior-compatible. `NewBare` controls runtime
wiring; it does not yet make the package import graph minimal.

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
boundary discussion and `docs/sdk-gate-request.md` for the programmable gate
request field contract.
