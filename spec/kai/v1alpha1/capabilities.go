package kaiv1alpha1

import "strings"

const (
	// ContractVersion is the KAI v1alpha1 wire contract version.
	ContractVersion = "v1alpha1"

	CapabilityApply            = "apply"
	CapabilityConvergence      = "convergence"
	CapabilityObserve          = "observe"
	CapabilityRollback         = "rollback"
	CapabilityDelta            = "delta"
	CapabilitySubstrateObjects = "substrateobjects"
	CapabilityDryRun           = "dry-run"

	// Deprecated: use CapabilitySubstrateObjects.
	CapabilityBackendObjects = CapabilitySubstrateObjects
)

// HasCapability reports whether names contains required. Capability names may
// be plain ("apply") or vendor-qualified ("argocd.application.apply").
func HasCapability(names []string, required string) bool {
	required = normalizeCapability(required)
	if required == "" {
		return false
	}
	for _, name := range names {
		name = normalizeCapability(name)
		if name == required || strings.HasSuffix(name, "."+required) {
			return true
		}
	}
	return false
}

// HasAnyCapability reports whether names contains any required capability.
func HasAnyCapability(names []string, required ...string) bool {
	for _, candidate := range required {
		if HasCapability(names, candidate) {
			return true
		}
	}
	return false
}

func normalizeCapability(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, "backend-objects", CapabilitySubstrateObjects)
	name = strings.ReplaceAll(name, "backend-object", CapabilitySubstrateObjects)
	name = strings.ReplaceAll(name, "substrate-objects", CapabilitySubstrateObjects)
	name = strings.ReplaceAll(name, "substrate-object", CapabilitySubstrateObjects)
	if name == "backendobject" || name == "backendobjects" || name == "substrateobject" || name == "substrateobjects" {
		return CapabilitySubstrateObjects
	}
	if name == "dryrun" {
		return CapabilityDryRun
	}
	return name
}
