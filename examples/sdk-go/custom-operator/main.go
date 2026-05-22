package main

import (
	"context"
	"log"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/kapro/server"
)

type businessHoursGate struct{}

func (businessHoursGate) Evaluate(_ context.Context, _ gate.Request) (gate.Result, error) {
	now := time.Now().UTC()
	if now.Hour() >= 8 && now.Hour() < 18 {
		return gate.Result{Phase: kaprov1alpha2.GatePhasePassed, Message: "inside business hours"}, nil
	}
	return gate.Result{
		Phase:      kaprov1alpha2.GatePhaseInconclusive,
		Message:    "outside business hours",
		RetryAfter: time.Until(now.Add(30 * time.Minute)).String(),
	}, nil
}

func main() {
	s, err := server.New(server.OptionsFromEnv())
	if err != nil {
		log.Fatal(err)
	}

	s.Gates.MustRegister("business-hours", businessHoursGate{})

	if err := s.Run(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}
