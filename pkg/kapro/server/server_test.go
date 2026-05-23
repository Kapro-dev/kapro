package server

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/kapro/actuator"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
)

type stubActuator struct{}

func (stubActuator) Apply(context.Context, actuator.ApplyRequest) error { return nil }
func (stubActuator) IsConverged(context.Context, *kaprov1alpha2.Cluster, string, string) (bool, error) {
	return true, nil
}
func (stubActuator) Rollback(context.Context, *kaprov1alpha2.Cluster, string, string) error {
	return nil
}
func (stubActuator) ApplyDelta(context.Context, actuator.DeltaApplyRequest) (int, error) {
	return 0, nil
}
func (stubActuator) IsAllConverged(context.Context, *kaprov1alpha2.Cluster, map[string]string) (bool, error) {
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
	for _, name := range []string{"push/flux", "pull/flux", "pull/oci", "push/argo", "pull/argo"} {
		if _, err := srv.Actuators.Resolve(name); err != nil {
			t.Fatalf("resolve actuator %s: %v", name, err)
		}
	}
	reg, ok := srv.Actuators.Registration("push/argo")
	if !ok {
		t.Fatalf("push/argo registration metadata missing")
	}
	if reg.Capabilities.Driver != kaprov1alpha2.BackendDriverArgo || !reg.Capabilities.SupportsBackendObjects {
		t.Fatalf("push/argo capabilities = %#v", reg.Capabilities)
	}
	for _, name := range []string{"approval", "soak", "webhook"} {
		if _, err := srv.Gates.Resolve(name); err != nil {
			t.Fatalf("resolve gate %s: %v", name, err)
		}
	}
	for _, driver := range []kaprov1alpha2.BackendDriver{
		kaprov1alpha2.BackendDriverArgo,
		kaprov1alpha2.BackendDriverFlux,
		kaprov1alpha2.BackendDriverOCI,
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
					Driver:        kaprov1alpha2.BackendDriverExternal,
					Adapter:       "external",
					Runtime:       kaprov1alpha2.BackendRuntimeHub,
					Modes:         []kaprov1alpha2.DeliveryMode{kaprov1alpha2.DeliveryModePush},
					SupportsApply: true,
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
				kaprov1alpha2.BackendDriverExternal,
				kaprov1alpha2.BackendRuntimeBoth,
				kaproadapter.DiscoveryModel{Supported: true},
			)),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.Adapters.Resolve(kaprov1alpha2.BackendDriverExternal); err != nil {
		t.Fatalf("resolve custom adapter: %v", err)
	}
	if _, err := srv.Adapters.Resolve(kaprov1alpha2.BackendDriverFlux); err == nil {
		t.Fatalf("default adapter registered despite custom registrar override")
	}
}
