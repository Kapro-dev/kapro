package kaiv1alpha1

import "testing"

func TestHasCapabilityAcceptsPlainAndQualifiedNames(t *testing.T) {
	capabilities := []string{
		"argocd.application.targetRevision.apply",
		"argocd.application.sync-health.convergence",
		"argocd.application.backend-object",
		"dryrun",
	}
	for _, required := range []string{CapabilityApply, CapabilityConvergence, CapabilityBackendObjects, CapabilityDryRun} {
		if !HasCapability(capabilities, required) {
			t.Fatalf("HasCapability(%q) = false, want true", required)
		}
	}
}

func TestHasAnyCapability(t *testing.T) {
	if !HasAnyCapability([]string{"observe"}, CapabilityConvergence, CapabilityObserve) {
		t.Fatal("observe should satisfy convergence/observe capability set")
	}
	if HasAnyCapability([]string{"apply"}, CapabilityRollback, CapabilityDelta) {
		t.Fatal("apply should not satisfy rollback/delta capability set")
	}
}
