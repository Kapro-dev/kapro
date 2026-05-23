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

func TestContractPolicies(t *testing.T) {
	tests := []struct {
		name       string
		pluginType kaprov1alpha2.PluginType
		contract   string
	}{
		{name: "kai", pluginType: kaprov1alpha2.PluginTypeActuator, contract: "KAI"},
		{name: "kgi", pluginType: kaprov1alpha2.PluginTypeGate, contract: "KGI"},
		{name: "kpi", pluginType: kaprov1alpha2.PluginTypePlanner, contract: "KPI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policies := ContractPolicies(tt.pluginType)
			if len(policies) != 1 {
				t.Fatalf("ContractPolicies(%q) = %v", tt.pluginType, policies)
			}
			policy := policies[0]
			if policy.PluginType != tt.pluginType {
				t.Fatalf("PluginType = %q, want %q", policy.PluginType, tt.pluginType)
			}
			if policy.Contract != tt.contract {
				t.Fatalf("Contract = %q, want %q", policy.Contract, tt.contract)
			}
			if policy.Version != VersionV1Alpha1 {
				t.Fatalf("Version = %q, want %q", policy.Version, VersionV1Alpha1)
			}
			if policy.SupportLevel != SupportLevelSupported {
				t.Fatalf("SupportLevel = %q, want %q", policy.SupportLevel, SupportLevelSupported)
			}
			if policy.Since == "" {
				t.Fatal("Since is empty")
			}
			if got, ok := SupportedContractPolicy(tt.pluginType, VersionV1Alpha1); !ok || got != policy {
				t.Fatalf("SupportedContractPolicy(%q, %q) = (%#v, %v), want (%#v, true)", tt.pluginType, VersionV1Alpha1, got, ok, policy)
			}
		})
	}
}

func TestContractPoliciesAreDefensiveCopies(t *testing.T) {
	versions := SupportedContractVersions(kaprov1alpha2.PluginTypeActuator)
	versions[0] = "mutated"
	if got := SupportedContractVersionsString(kaprov1alpha2.PluginTypeActuator); got != VersionV1Alpha1 {
		t.Fatalf("SupportedContractVersionsString = %q, want %q", got, VersionV1Alpha1)
	}

	policies := ContractPolicies(kaprov1alpha2.PluginTypeActuator)
	policies[0].Version = "mutated"
	if got, ok := SupportedContractPolicy(kaprov1alpha2.PluginTypeActuator, VersionV1Alpha1); !ok || got.Version != VersionV1Alpha1 {
		t.Fatalf("SupportedContractPolicy after mutation = (%#v, %v)", got, ok)
	}
}

func TestUnknownPluginTypeHasNoPolicy(t *testing.T) {
	if got := SupportedContractVersions("unknown"); got != nil {
		t.Fatalf("SupportedContractVersions(unknown) = %v, want nil", got)
	}
	if got := ContractPolicies("unknown"); got != nil {
		t.Fatalf("ContractPolicies(unknown) = %v, want nil", got)
	}
	if _, ok := SupportedContractPolicy("unknown", VersionV1Alpha1); ok {
		t.Fatal("SupportedContractPolicy(unknown) returned ok=true")
	}
}
