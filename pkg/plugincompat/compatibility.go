// Package plugincompat defines Kapro plugin contract compatibility policy.
//
// Two version axes are tracked at handshake (see proto GetCapabilitiesResponse):
//
//   - contract_version: the KAI/KGI/KPI wire contract implemented by the plugin.
//     This is what Kapro enforces. Plugins reporting an unsupported value are
//     rejected by internal/plugin/probe (validateContract) and surface
//     Compatible=False on Plugin.status.
//   - plugin_version: free-form, plugin-author-owned implementation version.
//     Recorded on status for operators, never used in admission decisions.
//
// Adding a new contract version (e.g. v1alpha2) means:
//  1. add the constant, supported-list entry, and ContractPolicy entry here;
//  2. update docs/plugin-compatibility-policy.md and docs/plugin-authoring.md;
//  3. ship the matching conformance/* harness for the new version.
//
// The full lifecycle (deprecation window, removal, discovery) is documented in
// docs/plugin-compatibility-policy.md.
package plugincompat

import (
	"strings"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	// VersionV1Alpha1 is the initial KAI/KGI/KPI contract version.
	VersionV1Alpha1 = "v1alpha1"
)

// SupportLevel describes the lifecycle state of a plugin contract version.
type SupportLevel string

const (
	// SupportLevelSupported means Kapro accepts the contract version at probe
	// time and ships a matching conformance suite.
	SupportLevelSupported SupportLevel = "supported"
)

// ContractPolicy describes one plugin contract version Kapro knows about.
// Compatibility is decided from ContractVersion, never from plugin_version.
type ContractPolicy struct {
	PluginType   kaprov1alpha2.PluginType
	Contract     string
	Version      string
	SupportLevel SupportLevel
	Since        string
	DeprecatedIn string
	RemovedAfter string
}

var (
	supportedKAIContractVersions = []string{VersionV1Alpha1}
	supportedKGIContractVersions = []string{VersionV1Alpha1}
	supportedKPIContractVersions = []string{VersionV1Alpha1}

	kaiContractPolicies = []ContractPolicy{{
		PluginType:   kaprov1alpha2.PluginTypeActuator,
		Contract:     "KAI",
		Version:      VersionV1Alpha1,
		SupportLevel: SupportLevelSupported,
		Since:        "v0.3.0",
	}}
	kgiContractPolicies = []ContractPolicy{{
		PluginType:   kaprov1alpha2.PluginTypeGate,
		Contract:     "KGI",
		Version:      VersionV1Alpha1,
		SupportLevel: SupportLevelSupported,
		Since:        "v0.3.0",
	}}
	kpiContractPolicies = []ContractPolicy{{
		PluginType:   kaprov1alpha2.PluginTypePlanner,
		Contract:     "KPI",
		Version:      VersionV1Alpha1,
		SupportLevel: SupportLevelSupported,
		Since:        "v0.3.0",
	}}
)

// SupportedKAIContractVersions returns the KAI contract versions supported by this release.
func SupportedKAIContractVersions() []string {
	return append([]string(nil), supportedKAIContractVersions...)
}

// SupportedKGIContractVersions returns the KGI contract versions supported by this release.
func SupportedKGIContractVersions() []string {
	return append([]string(nil), supportedKGIContractVersions...)
}

// SupportedKPIContractVersions returns the KPI contract versions supported by this release.
func SupportedKPIContractVersions() []string {
	return append([]string(nil), supportedKPIContractVersions...)
}

// SupportedContractVersions returns the contract versions supported for a plugin type.
func SupportedContractVersions(pluginType kaprov1alpha2.PluginType) []string {
	switch pluginType {
	case kaprov1alpha2.PluginTypeActuator:
		return SupportedKAIContractVersions()
	case kaprov1alpha2.PluginTypeGate:
		return SupportedKGIContractVersions()
	case kaprov1alpha2.PluginTypePlanner:
		return SupportedKPIContractVersions()
	default:
		return nil
	}
}

// ContractPolicies returns the compatibility policy entries for pluginType.
func ContractPolicies(pluginType kaprov1alpha2.PluginType) []ContractPolicy {
	switch pluginType {
	case kaprov1alpha2.PluginTypeActuator:
		return append([]ContractPolicy(nil), kaiContractPolicies...)
	case kaprov1alpha2.PluginTypeGate:
		return append([]ContractPolicy(nil), kgiContractPolicies...)
	case kaprov1alpha2.PluginTypePlanner:
		return append([]ContractPolicy(nil), kpiContractPolicies...)
	default:
		return nil
	}
}

// SupportedContractPolicy returns the supported policy entry for version.
func SupportedContractPolicy(pluginType kaprov1alpha2.PluginType, version string) (ContractPolicy, bool) {
	for _, policy := range ContractPolicies(pluginType) {
		if policy.Version == version && policy.SupportLevel == SupportLevelSupported {
			return policy, true
		}
	}
	return ContractPolicy{}, false
}

// IsContractVersionSupported returns true when version is supported for pluginType.
func IsContractVersionSupported(pluginType kaprov1alpha2.PluginType, version string) bool {
	_, ok := SupportedContractPolicy(pluginType, version)
	return ok
}

// ContractName returns the short interface name for a plugin type.
func ContractName(pluginType kaprov1alpha2.PluginType) string {
	switch pluginType {
	case kaprov1alpha2.PluginTypeActuator:
		return "KAI"
	case kaprov1alpha2.PluginTypeGate:
		return "KGI"
	case kaprov1alpha2.PluginTypePlanner:
		return "KPI"
	default:
		return string(pluginType)
	}
}

// SupportedContractVersionsString returns a comma-separated version list for messages.
func SupportedContractVersionsString(pluginType kaprov1alpha2.PluginType) string {
	return strings.Join(SupportedContractVersions(pluginType), ", ")
}
