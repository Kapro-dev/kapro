package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/spokeprovider"
)

type fakeProvider struct {
	caps spokeprovider.Capabilities
	res  spokeprovider.ReconcileResult
}

func (p fakeProvider) Driver() v1alpha2.BackendDriver { return p.caps.Driver }
func (p fakeProvider) Capabilities() spokeprovider.Capabilities {
	return p.caps
}
func (p fakeProvider) Reconcile(context.Context, spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	return p.res
}

func TestCheckPasses(t *testing.T) {
	report := Check(context.Background(), fakeProvider{
		caps: spokeprovider.Capabilities{
			Driver:            v1alpha2.BackendDriverOCI,
			SupportsReconcile: true,
			SupportsObserve:   true,
		},
		res: spokeprovider.ReconcileResult{Phase: v1alpha2.DeliveryPhaseConverged},
	}, DefaultScenario())
	if !report.Passed() {
		t.Fatalf("report failed: %#v", report.Failed())
	}
}

func TestCheckReportsMissingCapability(t *testing.T) {
	report := Check(context.Background(), fakeProvider{
		caps: spokeprovider.Capabilities{Driver: v1alpha2.BackendDriverOCI},
		res:  spokeprovider.ReconcileResult{Phase: v1alpha2.DeliveryPhaseConverged},
	}, DefaultScenario())
	if report.Passed() {
		t.Fatalf("report passed unexpectedly")
	}
	for _, result := range report.Failed() {
		if result.Name == "CapabilitiesReportSupportedContract" &&
			strings.Contains(result.Message, spokeprovider.CapabilityReconcile) {
			return
		}
	}
	t.Fatalf("missing capability failure not reported: %#v", report.Failed())
}

func TestCheckReportsNonDeterministicShape(t *testing.T) {
	report := Check(context.Background(), &togglingProvider{}, DefaultScenario())
	if report.Passed() {
		t.Fatalf("report passed unexpectedly")
	}
	for _, result := range report.Failed() {
		if result.Name == "ReconcileHasDeterministicShape" {
			return
		}
	}
	t.Fatalf("determinism failure not reported: %#v", report.Failed())
}

type togglingProvider struct {
	calls int
}

func (p *togglingProvider) Driver() v1alpha2.BackendDriver { return v1alpha2.BackendDriverOCI }

func (p *togglingProvider) Capabilities() spokeprovider.Capabilities {
	return spokeprovider.Capabilities{
		Driver:            v1alpha2.BackendDriverOCI,
		SupportsReconcile: true,
	}
}

func (p *togglingProvider) Reconcile(context.Context, spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	p.calls++
	if p.calls%2 == 0 {
		return spokeprovider.ReconcileResult{Phase: v1alpha2.DeliveryPhaseFailed, Err: errors.New("changed")}
	}
	return spokeprovider.ReconcileResult{Phase: v1alpha2.DeliveryPhaseConverged}
}
