package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
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

	s.Gates.MustRegister("canary-error-rate", gate.Func(func(_ context.Context, req gate.Request) (gate.Result, error) {
		threshold := parseFloat(req.Parameters["threshold"], 0.001)
		observed := parseFloat(req.Parameters["observed"], 0)
		if observed < threshold {
			return gate.MakePassed(fmt.Sprintf("error rate %.4f < %.4f", observed, threshold)), nil
		}
		return gate.MakeFailed("ErrorRateExceeded", "error rate %.4f >= %.4f", observed, threshold), nil
	}))

	s.Gates.MustRegister("external-readiness", gate.Func(func(ctx context.Context, req gate.Request) (gate.Result, error) {
		url := req.Parameters["url"]
		if url == "" {
			return gate.MakeFailed("MissingURL", "url parameter is required"), nil
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return gate.MakeFailed("InvalidURL", "%v", err), nil
		}
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return gate.MakePending("UpstreamUnavailable", time.Now().Add(15*time.Second)), nil
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return gate.MakePassed("upstream ready"), nil
		}
		return gate.MakeFailed("UpstreamNotReady", "status %d", resp.StatusCode), nil
	}))

	if err := s.Run(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}

func parseFloat(raw string, fallback float64) float64 {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return v
}
