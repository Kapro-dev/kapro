// Package provider provides conformance checks for KSP spoke providers.
package provider

import (
	"context"
	"fmt"
	"testing"
	"time"

	"kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/conformance"
	"kapro.io/kapro/pkg/spokeprovider"
)

type Scenario struct {
	Request              spokeprovider.ReconcileRequest
	RequiredCapabilities []string
	Timeout              time.Duration
}

func DefaultScenario() Scenario {
	return Scenario{
		Request: spokeprovider.ReconcileRequest{
			Cluster: &v1alpha2.Cluster{},
			AppKey:  "conformance",
			BackendProfile: &v1alpha2.Backend{
				Spec: v1alpha2.BackendSpec{Driver: v1alpha2.BackendDriverOCI},
			},
			DesiredVersion: "v1.0.0",
			Parameters:     map[string]string{"conformance": "true"},
		},
		RequiredCapabilities: []string{spokeprovider.CapabilityReconcile},
		Timeout:              10 * time.Second,
	}
}

func Run(t *testing.T, p spokeprovider.Provider, scenario Scenario) {
	t.Helper()
	report := Check(context.Background(), p, scenario)
	for _, result := range report.Results {
		if !result.Passed {
			t.Fatalf("%s: %s", result.Name, result.Message)
		}
	}
}

func Check(ctx context.Context, p spokeprovider.Provider, scenario Scenario) conformance.Report {
	if ctx == nil {
		ctx = context.Background()
	}
	if scenario.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, scenario.Timeout)
		defer cancel()
	}
	return conformance.Report{
		Suite: "KSP provider",
		Results: []conformance.Result{
			checkCapabilities(p, scenario),
			checkReconcileNoPanic(ctx, p, scenario),
			checkReconcileDeterministicShape(ctx, p, scenario),
		},
	}
}

func checkCapabilities(p spokeprovider.Provider, scenario Scenario) conformance.Result {
	const name = "CapabilitiesReportSupportedContract"
	if p == nil {
		return conformance.Fail(name, "provider is nil")
	}
	caps := p.Capabilities().Normalize()
	if caps.ContractVersion != spokeprovider.ContractVersionV1Alpha1 {
		return conformance.Fail(name, "contract_version=%q, want %q", caps.ContractVersion, spokeprovider.ContractVersionV1Alpha1)
	}
	if caps.Driver == "" {
		return conformance.Fail(name, "driver is empty")
	}
	if missing := missingCapabilities(caps, requiredCapabilities(scenario)); len(missing) > 0 {
		return conformance.Fail(name, "missing required capabilities %v", missing)
	}
	return conformance.Pass(name)
}

func checkReconcileNoPanic(ctx context.Context, p spokeprovider.Provider, scenario Scenario) (result conformance.Result) {
	const name = "ReconcileDoesNotPanic"
	res, recovered := safeReconcile(ctx, p, scenario.Request)
	if recovered != nil {
		return conformance.Fail(name, "panic: %v", recovered)
	}
	if res.Phase == "" {
		return conformance.Fail(name, "result phase is empty")
	}
	return conformance.Pass(name)
}

func checkReconcileDeterministicShape(ctx context.Context, p spokeprovider.Provider, scenario Scenario) conformance.Result {
	const name = "ReconcileHasDeterministicShape"
	first, recovered := safeReconcile(ctx, p, scenario.Request)
	if recovered != nil {
		return conformance.Fail(name, "first reconcile panic: %v", recovered)
	}
	second, recovered := safeReconcile(ctx, p, scenario.Request)
	if recovered != nil {
		return conformance.Fail(name, "second reconcile panic: %v", recovered)
	}
	if first.Phase != second.Phase ||
		first.Format != second.Format ||
		first.ObservedDigest != second.ObservedDigest ||
		first.AppliedObjects != second.AppliedObjects ||
		errorString(first.Err) != errorString(second.Err) {
		return conformance.Fail(name, "first=%#v second=%#v", first, second)
	}
	return conformance.Pass(name)
}

func safeReconcile(ctx context.Context, p spokeprovider.Provider, req spokeprovider.ReconcileRequest) (result spokeprovider.ReconcileResult, recovered any) {
	defer func() {
		recovered = recover()
	}()
	return p.Reconcile(ctx, req), nil
}

func requiredCapabilities(scenario Scenario) []string {
	if len(scenario.RequiredCapabilities) > 0 {
		return append([]string(nil), scenario.RequiredCapabilities...)
	}
	return []string{spokeprovider.CapabilityReconcile}
}

func missingCapabilities(caps spokeprovider.Capabilities, required []string) []string {
	missing := make([]string, 0)
	for _, capability := range required {
		switch capability {
		case spokeprovider.CapabilityReconcile:
			if !caps.SupportsReconcile {
				missing = append(missing, capability)
			}
		case spokeprovider.CapabilityObserve:
			if !caps.SupportsObserve {
				missing = append(missing, capability)
			}
		case spokeprovider.CapabilityApply:
			if !caps.SupportsApply {
				missing = append(missing, capability)
			}
		case spokeprovider.CapabilityDryRun:
			if !caps.SupportsDryRun {
				missing = append(missing, capability)
			}
		default:
			missing = append(missing, fmt.Sprintf("unknown:%s", capability))
		}
	}
	return missing
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
