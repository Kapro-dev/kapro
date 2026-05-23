package controllermanager

import "testing"

func TestBuildGateRegistryIncludesMaxDrift(t *testing.T) {
	reg, err := BuildGateRegistry(nil)
	if err != nil {
		t.Fatalf("BuildGateRegistry: %v", err)
	}
	for _, name := range []string{"max-drift", "maxdrift"} {
		if _, err := reg.Resolve(name); err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
	}
}
