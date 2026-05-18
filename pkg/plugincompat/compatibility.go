// Package plugincompat defines Kapro plugin contract compatibility policy.
//
// Two version axes are tracked at handshake (see proto GetCapabilitiesResponse):
//
//   - contract_version: the KAI/KGI/KPI wire contract implemented by the plugin.
//     This is what Kapro enforces. Plugins reporting an unsupported value are
//     rejected by internal/plugin/probe (validateContract) and surface
//     Compatible=False on PluginRegistration.status.
//   - plugin_version: free-form, plugin-author-owned implementation version.
//     Recorded on status for operators, never used in admission decisions.
//
// Adding a new contract version (e.g. v1alpha2) means:
//  1. add the constant + supported-list entry here;
//  2. update docs/plugin-compatibility.md (the human-facing matrix);
//  3. ship the matching conformance/* harness for the new version.
//
// The full lifecycle (deprecation window, removal, discovery) is documented in
// docs/plugin-compatibility.md.
package plugincompat

import (
	"strings"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	// VersionV1Alpha1 is the initial KAI/KGI/KPI contract version.
	VersionV1Alpha1 = "v1alpha1"
)

var (
	supportedKAIContractVersions = []string{VersionV1Alpha1}
	supportedKGIContractVersions = []string{VersionV1Alpha1}
	supportedKPIContractVersions = []string{VersionV1Alpha1}
)

// SupportedKAIContractVersions returns the KAI contract versions supported by this promotionrun.
func SupportedKAIContractVersions() []string {
	return append([]string(nil), supportedKAIContractVersions...)
}

// SupportedKGIContractVersions returns the KGI contract versions supported by this promotionrun.
func SupportedKGIContractVersions() []string {
	return append([]string(nil), supportedKGIContractVersions...)
}

// SupportedKPIContractVersions returns the KPI contract versions supported by this promotionrun.
func SupportedKPIContractVersions() []string {
	return append([]string(nil), supportedKPIContractVersions...)
}

// SupportedContractVersions returns the contract versions supported for a plugin type.
func SupportedContractVersions(pluginType kaprov1alpha1.PluginType) []string {
	switch pluginType {
	case kaprov1alpha1.PluginTypeActuator:
		return SupportedKAIContractVersions()
	case kaprov1alpha1.PluginTypeGate:
		return SupportedKGIContractVersions()
	case kaprov1alpha1.PluginTypePlanner:
		return SupportedKPIContractVersions()
	default:
		return nil
	}
}

// IsContractVersionSupported returns true when version is supported for pluginType.
func IsContractVersionSupported(pluginType kaprov1alpha1.PluginType, version string) bool {
	for _, supported := range SupportedContractVersions(pluginType) {
		if version == supported {
			return true
		}
	}
	return false
}

// ContractName returns the short interface name for a plugin type.
func ContractName(pluginType kaprov1alpha1.PluginType) string {
	switch pluginType {
	case kaprov1alpha1.PluginTypeActuator:
		return "KAI"
	case kaprov1alpha1.PluginTypeGate:
		return "KGI"
	case kaprov1alpha1.PluginTypePlanner:
		return "KPI"
	default:
		return string(pluginType)
	}
}

// SupportedContractVersionsString returns a comma-separated version list for messages.
func SupportedContractVersionsString(pluginType kaprov1alpha1.PluginType) string {
	return strings.Join(SupportedContractVersions(pluginType), ", ")
}
