package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestRuntimeSchemeRegistersControllerOwnedKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	for _, kind := range []string{"DecisionTrace", "PromotionRun", "Target"} {
		if _, err := scheme.New(GroupVersion.WithKind(kind)); err != nil {
			t.Errorf("runtime kind %q not registered: %v", kind, err)
		}
	}
}
