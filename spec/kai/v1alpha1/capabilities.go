package kaiv1alpha1

import "strings"

const (
	// ContractVersion is the KAI v1alpha1 wire contract version.
	ContractVersion = "v1alpha1"

	CapabilityApply          = "apply"
	CapabilityConvergence    = "convergence"
	CapabilityObserve        = "observe"
	CapabilityRollback       = "rollback"
	CapabilityDelta          = "delta"
	CapabilityBackendObjects = "backendobjects"
	CapabilityDryRun         = "dry-run"
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
	name = strings.ReplaceAll(name, "backend-object", CapabilityBackendObjects)
	if name == "backendobject" {
		return CapabilityBackendObjects
	}
	if name == "dryrun" {
		return CapabilityDryRun
	}
	return name
}
