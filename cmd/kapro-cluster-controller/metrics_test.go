package main

import "testing"

func TestMetricsDisabled(t *testing.T) {
	for _, addr := range []string{"", "off", "OFF", "disabled", "none"} {
		if !metricsDisabled(addr) {
			t.Fatalf("metricsDisabled(%q)=false, want true", addr)
		}
	}
	if metricsDisabled(":8080") {
		t.Fatal("metricsDisabled(:8080)=true, want false")
	}
}
