package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/spokeprovider"
)

type fakeProvider struct {
	caps spokeprovider.Capabilities
	res  spokeprovider.ReconcileResult
}

func (p fakeProvider) SubstrateKind() v1alpha1.SubstrateKind { return p.caps.SubstrateKind }
func (p fakeProvider) Capabilities() spokeprovider.Capabilities {
	return p.caps
}
func (p fakeProvider) Reconcile(context.Context, spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	return p.res
}

func TestCheckPasses(t *testing.T) {
	report := Check(context.Background(), fakeProvider{
		caps: spokeprovider.Capabilities{
			SubstrateKind:     v1alpha1.SubstrateKindOCI,
			SupportsReconcile: true,
			SupportsObserve:   true,
		},
		res: spokeprovider.ReconcileResult{Phase: v1alpha1.DeliveryPhaseConverged},
	}, DefaultScenario())
	if !report.Passed() {
		t.Fatalf("report failed: %#v", report.Failed())
	}
}

func TestCheckDefaultsScenarioDriverFromProvider(t *testing.T) {
	report := Check(context.Background(), driverCheckingProvider{
		driver: v1alpha1.SubstrateKindFlux,
	}, DefaultScenario())
	if !report.Passed() {
		t.Fatalf("report failed: %#v", report.Failed())
	}
}

func TestCheckReportsMissingCapability(t *testing.T) {
	report := Check(context.Background(), fakeProvider{
		caps: spokeprovider.Capabilities{SubstrateKind: v1alpha1.SubstrateKindOCI},
		res:  spokeprovider.ReconcileResult{Phase: v1alpha1.DeliveryPhaseConverged},
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

func (p *togglingProvider) SubstrateKind() v1alpha1.SubstrateKind { return v1alpha1.SubstrateKindOCI }

func (p *togglingProvider) Capabilities() spokeprovider.Capabilities {
	return spokeprovider.Capabilities{
		SubstrateKind:     v1alpha1.SubstrateKindOCI,
		SupportsReconcile: true,
	}
}

func (p *togglingProvider) Reconcile(context.Context, spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	p.calls++
	if p.calls%2 == 0 {
		return spokeprovider.ReconcileResult{Phase: v1alpha1.DeliveryPhaseFailed, Err: errors.New("changed")}
	}
	return spokeprovider.ReconcileResult{Phase: v1alpha1.DeliveryPhaseConverged}
}

type driverCheckingProvider struct {
	driver v1alpha1.SubstrateKind
}

func (p driverCheckingProvider) SubstrateKind() v1alpha1.SubstrateKind { return p.driver }

func (p driverCheckingProvider) Capabilities() spokeprovider.Capabilities {
	return spokeprovider.Capabilities{
		SubstrateKind:     p.driver,
		SupportsReconcile: true,
	}
}

func (p driverCheckingProvider) Reconcile(_ context.Context, req spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	if req.SubstrateProfile == nil || req.SubstrateProfile.Spec.SubstrateKind() != string(p.driver) {
		panic("scenario driver did not match provider")
	}
	return spokeprovider.ReconcileResult{Phase: v1alpha1.DeliveryPhaseConverged}
}
