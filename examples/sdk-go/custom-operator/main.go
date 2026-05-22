package main

import (
	"context"
	"log"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"kapro.io/kapro/pkg/kapro/gate"
	"kapro.io/kapro/pkg/kapro/server"
)

func main() {
	s, err := server.New(server.OptionsFromEnv())
	if err != nil {
		log.Fatal(err)
	}

	s.Gates.MustRegister("business-hours", gate.Func(func(_ context.Context, _ gate.Request) (gate.Result, error) {
		now := time.Now().UTC()
		if now.Hour() >= 8 && now.Hour() < 18 {
			return gate.MakePassed("inside business hours"), nil
		}
		return gate.MakePending("OutsideBusinessHours", now.Add(30*time.Minute)), nil
	}))

	if err := s.Run(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}
