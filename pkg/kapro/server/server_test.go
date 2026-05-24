package server

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/kapro/actuator"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
)

type stubActuator struct{}

func (stubActuator) Apply(context.Context, actuator.ApplyRequest) error { return nil }
func (stubActuator) IsConverged(context.Context, *kaprov1alpha1.Cluster, string, string) (bool, error) {
	return true, nil
}
func (stubActuator) Rollback(context.Context, *kaprov1alpha1.Cluster, string, string) error {
	return nil
}
func (stubActuator) ApplyDelta(context.Context, actuator.DeltaApplyRequest) (int, error) {
	return 0, nil
}
func (stubActuator) IsAllConverged(context.Context, *kaprov1alpha1.Cluster, map[string]string) (bool, error) {
	return true, nil
}

func TestDefaultLeaderElectionIDForShardIsDNS1123Safe(t *testing.T) {
	id := defaultLeaderElectionID("prod_eu/1")
	if id == "kapro-operator-leader-prod_eu/1.kapro.io" {
		t.Fatal("leader election ID should not embed raw shard name")
	}
	if strings.ContainsAny(id, "_/") {
		t.Fatalf("leader election ID contains invalid raw shard characters: %q", id)
	}
	if errs := validation.IsDNS1123Subdomain(id); len(errs) > 0 {
		t.Fatalf("leader election ID is not DNS-1123 safe: %q: %v", id, errs)
	}
}

func TestDefaultLeaderElectionIDKeepsUnshardedDefault(t *testing.T) {
	if got := defaultLeaderElectionID(""); got != "kapro-operator-leader.kapro.io" {
		t.Fatalf("default leader election ID = %q", got)
	}
}

func TestNewBareCreatesMinimalServer(t *testing.T) {
	t.Setenv("KAPRO_APPROVAL_SECRET", "")
	t.Setenv("KAPRO_ENABLE_PLUGIN_GATEWAY", "")

	srv, err := NewBare(Options{
		Config: &rest.Config{
			Host: "https://127.0.0.1:65535",
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
			},
		},
		DevMode:                true,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		WebhookPort:            19442,
	})
	if err != nil {
		t.Fatalf("NewBare: %v", err)
	}
	if srv.Manager == nil || srv.Gates == nil || srv.Actuators == nil || srv.Adapters == nil || srv.Planner == nil {
		t.Fatalf("bare server fields not initialized: %#v", srv)
	}
	if srv.controllerContext != nil {
		t.Fatalf("NewBare initialized controller context")
	}
	if srv.opts.WebhookCertDir != "" {
		t.Fatalf("NewBare generated webhook cert dir %q", srv.opts.WebhookCertDir)
	}
	if _, err := srv.Gates.Resolve("soak"); err == nil {
		t.Fatalf("NewBare registered built-in gates")
	}
	if _, err := srv.Actuators.Resolve("push/flux"); err == nil {
		t.Fatalf("NewBare registered built-in actuators")
	}
	if _, err := srv.Adapters.Resolve(kaprov1alpha1.SubstrateKindFlux); err == nil {
		t.Fatalf("NewBare registered built-in adapters")
	}
}

func TestRegistrarsMutateExistingRegistries(t *testing.T) {
	t.Setenv("KAPRO_CONTROLLERS", "not-a-controller")
	t.Setenv("KAPRO_APPROVAL_SECRET", "test-secret")
	t.Setenv("KAPRO_ENABLE_PLUGIN_GATEWAY", "")

	srv, err := NewBare(Options{
		Config: &rest.Config{
			Host: "https://127.0.0.1:65535",
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
			},
		},
		DevMode:                true,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		WebhookPort:            19443,
	})
	if err != nil {
		t.Fatalf("NewBare: %v", err)
	}

	gates := srv.Gates
	actuators := srv.Actuators
	adapters := srv.Adapters
	if err := RegisterControllers(context.Background(), srv); err != nil {
		t.Fatalf("RegisterControllers: %v", err)
	}
	if srv.controllerContext == nil {
		t.Fatalf("controller context was not initialized")
	}
	if srv.controllerContext.GateRegistry != gates ||
		srv.controllerContext.ActuatorRegistry != actuators ||
		srv.controllerContext.AdapterRegistry != adapters {
		t.Fatalf("controller context did not retain original registry pointers")
	}

	if err := RegisterGates(context.Background(), srv); err != nil {
		t.Fatalf("RegisterGates: %v", err)
	}
	if err := RegisterActuators(context.Background(), srv); err != nil {
		t.Fatalf("RegisterActuators: %v", err)
	}
	if err := RegisterAdapters(context.Background(), srv); err != nil {
		t.Fatalf("RegisterAdapters: %v", err)
	}
	if srv.Gates != gates || srv.Actuators != actuators || srv.Adapters != adapters {
		t.Fatalf("registrars replaced registry pointers")
	}
	if _, err := gates.Resolve("soak"); err != nil {
		t.Fatalf("original gate registry not populated: %v", err)
	}
	if _, err := actuators.Resolve("push/flux"); err != nil {
		t.Fatalf("original actuator registry not populated: %v", err)
	}
	if _, err := adapters.Resolve(kaprov1alpha1.SubstrateKindFlux); err != nil {
		t.Fatalf("original adapter registry not populated: %v", err)
	}
}

func TestRegistrarsTolerateManualServerWithManager(t *testing.T) {
	t.Setenv("KAPRO_APPROVAL_SECRET", "test-secret")
	t.Setenv("KAPRO_CONTROLLERS", "not-a-controller")

	base, err := NewBare(Options{
		Config: &rest.Config{
			Host: "https://127.0.0.1:65535",
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
			},
		},
		DevMode:                true,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		WebhookPort:            19444,
	})
	if err != nil {
		t.Fatalf("NewBare: %v", err)
	}
	manual := &Server{Manager: base.Manager}
	if err := RegisterControllers(context.Background(), manual); err != nil {
		t.Fatalf("RegisterControllers on manual server: %v", err)
	}
	if manual.Gates == nil || manual.Actuators == nil || manual.Adapters == nil || manual.Planner == nil {
		t.Fatalf("manual server dependencies not defaulted: %#v", manual)
	}
	if manual.podNamespace != "kapro-system" {
		t.Fatalf("manual pod namespace = %q", manual.podNamespace)
	}
}

func TestServerNewPopulatesRegistries(t *testing.T) {
	t.Setenv("KAPRO_CONTROLLERS", "plan")
	t.Setenv("KAPRO_DISABLE_WEBHOOKS", "true")
	t.Setenv("KAPRO_DISABLE_APPROVAL_SERVER", "true")
	t.Setenv("KAPRO_DISABLE_HUB_GATEWAY", "true")
	t.Setenv("KAPRO_APPROVAL_SECRET", "test-secret")
	// pluginadapter.EnableEnv is KAPRO_ENABLE_PLUGIN_GATEWAY (not
	// KAPRO_PLUGIN_GATEWAY_ENABLED) — clear it explicitly so the test
	// can't be influenced by ambient process state.
	t.Setenv("KAPRO_ENABLE_PLUGIN_GATEWAY", "")

	srv, err := New(Options{
		Config: &rest.Config{
			Host: "https://127.0.0.1:65535",
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
			},
		},
		DevMode:                true,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		WebhookPort:            19443,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.Manager == nil || srv.Gates == nil || srv.Actuators == nil || srv.Adapters == nil || srv.Planner == nil {
		t.Fatalf("server registries not populated: %#v", srv)
	}
	for _, name := range []string{"push/direct", "push/flux", "pull/flux", "pull/oci", "push/argo", "pull/argo"} {
		if _, err := srv.Actuators.Resolve(name); err != nil {
			t.Fatalf("resolve actuator %s: %v", name, err)
		}
	}
	direct, ok := srv.Actuators.Registration("push/direct")
	if !ok {
		t.Fatalf("push/direct registration metadata missing")
	}
	if direct.Capabilities.SubstrateKind != "kubernetes-apply" || !direct.Capabilities.SupportsSubstrateObjects {
		t.Fatalf("push/direct capabilities = %#v", direct.Capabilities)
	}
	reg, ok := srv.Actuators.Registration("push/argo")
	if !ok {
		t.Fatalf("push/argo registration metadata missing")
	}
	if reg.Capabilities.SubstrateKind != kaprov1alpha1.SubstrateKindArgo || !reg.Capabilities.SupportsSubstrateObjects {
		t.Fatalf("push/argo capabilities = %#v", reg.Capabilities)
	}
	oci, ok := srv.Actuators.Registration("pull/oci")
	if !ok {
		t.Fatalf("pull/oci registration metadata missing")
	}
	if oci.Capabilities.SupportsTwoPhase {
		t.Fatalf("pull/oci must not claim hub-side two-phase support: %#v", oci.Capabilities)
	}
	for _, name := range []string{"approval", "soak", "webhook"} {
		if _, err := srv.Gates.Resolve(name); err != nil {
			t.Fatalf("resolve gate %s: %v", name, err)
		}
	}
	for _, driver := range []kaprov1alpha1.SubstrateKind{
		kaprov1alpha1.SubstrateKindArgo,
		kaprov1alpha1.SubstrateKindFlux,
		kaprov1alpha1.SubstrateKindOCI,
	} {
		if _, err := srv.Adapters.Resolve(driver); err != nil {
			t.Fatalf("resolve adapter %s: %v", driver, err)
		}
	}
}

func TestServerNewUsesCustomActuatorRegistrars(t *testing.T) {
	t.Setenv("KAPRO_CONTROLLERS", "plan")
	t.Setenv("KAPRO_DISABLE_WEBHOOKS", "true")
	t.Setenv("KAPRO_DISABLE_APPROVAL_SERVER", "true")
	t.Setenv("KAPRO_DISABLE_HUB_GATEWAY", "true")
	t.Setenv("KAPRO_APPROVAL_SECRET", "test-secret")
	t.Setenv("KAPRO_ENABLE_PLUGIN_GATEWAY", "")

	srv, err := New(Options{
		Config: &rest.Config{
			Host: "https://127.0.0.1:65535",
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
			},
		},
		DevMode:                true,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		WebhookPort:            19444,
		ActuatorRegistrars: []ActuatorRegistrar{
			RegisterActuator(actuator.Registration{
				Name: "push/external",
				Capabilities: actuator.Capabilities{
					SubstrateKind:  kaprov1alpha1.SubstrateKindExternal,
					Actuator:       "external",
					ExecutionScope: kaprov1alpha1.ExecutionScopeHub,
					Modes:          []kaprov1alpha1.DeliveryMode{kaprov1alpha1.DeliveryModePush},
					SupportsApply:  true,
				},
				Actuator: stubActuator{},
			}),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.Actuators.Resolve("push/external"); err != nil {
		t.Fatalf("resolve custom actuator: %v", err)
	}
	if _, err := srv.Actuators.Resolve("push/flux"); err == nil {
		t.Fatalf("default actuator registered despite custom registrar override")
	}
}

func TestServerNewUsesCustomAdapterRegistrars(t *testing.T) {
	t.Setenv("KAPRO_CONTROLLERS", "plan")
	t.Setenv("KAPRO_DISABLE_WEBHOOKS", "true")
	t.Setenv("KAPRO_DISABLE_APPROVAL_SERVER", "true")
	t.Setenv("KAPRO_DISABLE_HUB_GATEWAY", "true")
	t.Setenv("KAPRO_APPROVAL_SECRET", "test-secret")
	t.Setenv("KAPRO_ENABLE_PLUGIN_GATEWAY", "")

	srv, err := New(Options{
		Config: &rest.Config{
			Host: "https://127.0.0.1:65535",
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
			},
		},
		DevMode:                true,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		WebhookPort:            19445,
		AdapterRegistrars: []AdapterRegistrar{
			RegisterAdapter(kaproadapter.NewReferenceAdapter(
				kaprov1alpha1.SubstrateKindExternal,
				kaprov1alpha1.ExecutionScopeBoth,
				kaproadapter.DiscoveryModel{Supported: true},
			)),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.Adapters.Resolve(kaprov1alpha1.SubstrateKindExternal); err != nil {
		t.Fatalf("resolve custom adapter: %v", err)
	}
	if _, err := srv.Adapters.Resolve(kaprov1alpha1.SubstrateKindFlux); err == nil {
		t.Fatalf("default adapter registered despite custom registrar override")
	}
}
