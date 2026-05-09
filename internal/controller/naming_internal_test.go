package controller

import "testing"

func TestSyncName_AvoidsHyphenCollisions(t *testing.T) {
	a := syncName("a", "b-c", "d", "e")
	b := syncName("a", "b", "c-d", "e")
	if a == b {
		t.Fatalf("expected distinct sync names, got collision %q", a)
	}
}
