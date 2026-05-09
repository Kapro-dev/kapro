// kapro-conformance validates all built-in KSI implementations against the
// conformance suites defined in conformance/{gate,actuator,provider}/.
//
// Run it as a regular Go test binary:
//
//	go test ./cmd/kapro-conformance/... -v -race
//
// Each built-in gate and actuator is exercised against the suite.
// Third-party plugins should ship their own conformance test that calls
// the same suites:
//
//	func TestMyGateConformance(t *testing.T) {
//	    gate.RunSuite(t, &MyGate{})
//	}
package conformance_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	conformanceactuator "kapro.io/kapro/conformance/actuator"
	conformancegate "kapro.io/kapro/conformance/gate"

	fluxopactuator "kapro.io/kapro/internal/actuator/fluxoperator"
	internalgate "kapro.io/kapro/internal/gate"
)

func fakeClient() *fake.ClientBuilder {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = kaprov1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().WithScheme(s)
}

// ---- Gate conformance -------------------------------------------------------

func TestSoakGate_Conformance(t *testing.T) {
	conformancegate.RunSuite(t, &internalgate.SoakGate{})
}

func TestMetricsGate_Conformance(t *testing.T) {
	conformancegate.RunSuite(t, &internalgate.MetricsGate{})
}

func TestApprovalGate_Conformance(t *testing.T) {
	conformancegate.RunSuite(t, &internalgate.ApprovalGate{Client: fakeClient().Build()})
}

func TestVerificationGate_Conformance(t *testing.T) {
	conformancegate.RunSuite(t, &internalgate.VerificationGate{})
}

// ---- Actuator conformance ---------------------------------------------------

func TestFluxOperatorActuator_Conformance(t *testing.T) {
	conformanceactuator.RunSuite(t, &fluxopactuator.FluxOperatorActuator{Client: fakeClient().Build()})
}
