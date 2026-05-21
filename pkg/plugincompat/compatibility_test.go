package plugincompat

import (
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestSupportedContractVersions(t *testing.T) {
	tests := []struct {
		name       string
		pluginType kaprov1alpha2.PluginType
		want       string
	}{
		{name: "kai", pluginType: kaprov1alpha2.PluginTypeActuator, want: VersionV1Alpha1},
		{name: "kgi", pluginType: kaprov1alpha2.PluginTypeGate, want: VersionV1Alpha1},
		{name: "kpi", pluginType: kaprov1alpha2.PluginTypePlanner, want: VersionV1Alpha1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SupportedContractVersions(tt.pluginType)
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("SupportedContractVersions(%q) = %v, want [%s]", tt.pluginType, got, tt.want)
			}
			if !IsContractVersionSupported(tt.pluginType, tt.want) {
				t.Fatalf("IsContractVersionSupported(%q, %q) = false", tt.pluginType, tt.want)
			}
			if IsContractVersionSupported(tt.pluginType, "v2") {
				t.Fatalf("IsContractVersionSupported(%q, v2) = true", tt.pluginType)
			}
		})
	}
}
