// Package plugincompat defines Kapro plugin contract compatibility policy.
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
