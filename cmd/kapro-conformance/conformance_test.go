// kapro-conformance validates all built-in KSI implementations against the
// conformance suites defined in conformance/{gate,actuator,provider}/.
//
// Run it as a regular Go test binary:
//
//	go test ./cmd/kapro-conformance/... -v -race
//
// Each built-in gate, actuator, and provider is exercised against the suite.
// Third-party plugins should ship their own conformance test that calls
// the same suites:
//
//	func TestMyGateConformance(t *testing.T) {
//	    gate.RunSuite(t, &MyGate{})
//	}
package conformance_test

import (
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	conformanceactuator "kapro.io/kapro/conformance/actuator"
	conformancegate "kapro.io/kapro/conformance/gate"
	conformanceprovider "kapro.io/kapro/conformance/provider"

	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	internalgate "kapro.io/kapro/internal/gate"
	kgatewaygate "kapro.io/kapro/internal/gate/kgateway"
	mlflowgate "kapro.io/kapro/internal/gate/mlflow"
	shadowgate "kapro.io/kapro/internal/gate/shadow"
	capiprovider "kapro.io/kapro/internal/provider/capi"
	ocmprovider "kapro.io/kapro/internal/provider/ocm"
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

func TestShadowGate_Conformance(t *testing.T) {
	conformancegate.RunSuite(t, &shadowgate.Gate{})
}

func TestKGatewayGate_Conformance(t *testing.T) {
	conformancegate.RunSuite(t, &kgatewaygate.Gate{})
}

func TestMLflowGate_Conformance(t *testing.T) {
	conformancegate.RunSuite(t, &mlflowgate.Gate{})
}

// ---- Actuator conformance ---------------------------------------------------

func TestFluxActuator_Conformance(t *testing.T) {
	conformanceactuator.RunSuite(t, &fluxactuator.FluxActuator{Client: fakeClient().Build()})
}

// ---- Provider conformance ---------------------------------------------------

// TestCAPIProvider_Conformance and TestOCMProvider_Conformance require a
// real Kubernetes API server (they use dynamic clients). They are skipped
// in unit test environments where KUBECONFIG is not set.
func TestCAPIProvider_Conformance(t *testing.T) {
	cfg, err := loadKubeconfig()
	if err != nil {
		t.Skipf("skipping CAPI provider conformance: no cluster available: %v", err)
	}
	p, err := capiprovider.New(cfg, fakeClient().Build())
	if err != nil {
		t.Skipf("skipping CAPI provider conformance: constructor failed: %v", err)
	}
	conformanceprovider.RunSuite(t, p)
}

func TestOCMProvider_Conformance(t *testing.T) {
	cfg, err := loadKubeconfig()
	if err != nil {
		t.Skipf("skipping OCM provider conformance: no cluster available: %v", err)
	}
	p, err := ocmprovider.New(cfg, fakeClient().Build())
	if err != nil {
		t.Skipf("skipping OCM provider conformance: constructor failed: %v", err)
	}
	conformanceprovider.RunSuite(t, p)
}

// loadKubeconfig returns a *rest.Config using KUBECONFIG env or in-cluster config.
// Returns an error if neither is available — used to skip cluster-dependent tests.
func loadKubeconfig() (*rest.Config, error) {
	kc := os.Getenv("KUBECONFIG")
	if kc == "" {
		kc = os.Getenv("HOME") + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		return rest.InClusterConfig()
	}
	return cfg, nil
}
