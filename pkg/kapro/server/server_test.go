package server

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
)

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
	if srv.Manager == nil || srv.Gates == nil || srv.Actuators == nil || srv.Planner == nil {
		t.Fatalf("server registries not populated: %#v", srv)
	}
	for _, name := range []string{"pull/flux", "pull/oci", "push/argo"} {
		if _, err := srv.Actuators.Resolve(name); err != nil {
			t.Fatalf("resolve actuator %s: %v", name, err)
		}
	}
	for _, name := range []string{"approval", "soak", "webhook"} {
		if _, err := srv.Gates.Resolve(name); err != nil {
			t.Fatalf("resolve gate %s: %v", name, err)
		}
	}
}
