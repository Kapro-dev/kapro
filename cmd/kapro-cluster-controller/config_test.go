package main

import (
	"flag"
	"os"
	"testing"
)

func TestLoadConfigMetricsAddrFromEnv(t *testing.T) {
	resetFlags(t, "kapro-cluster-controller")
	t.Setenv("KAPRO_CLUSTER_NAME", "prod")
	t.Setenv("KAPRO_HUB_URL", "https://hub.example.invalid")
	t.Setenv("KAPRO_METRICS_ADDR", "off")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.MetricsAddr != "off" {
		t.Fatalf("MetricsAddr=%q, want off", cfg.MetricsAddr)
	}
}

func TestLoadConfigMetricsAddrFlagOverridesEnv(t *testing.T) {
	resetFlags(t, "kapro-cluster-controller", "--metrics-addr=:9090")
	t.Setenv("KAPRO_CLUSTER_NAME", "prod")
	t.Setenv("KAPRO_HUB_URL", "https://hub.example.invalid")
	t.Setenv("KAPRO_METRICS_ADDR", "off")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.MetricsAddr != ":9090" {
		t.Fatalf("MetricsAddr=%q, want :9090", cfg.MetricsAddr)
	}
}

func resetFlags(t *testing.T, args ...string) {
	t.Helper()
	oldCommandLine := flag.CommandLine
	oldArgs := os.Args
	flag.CommandLine = flag.NewFlagSet(args[0], flag.ContinueOnError)
	os.Args = args
	t.Cleanup(func() {
		flag.CommandLine = oldCommandLine
		os.Args = oldArgs
	})
}
