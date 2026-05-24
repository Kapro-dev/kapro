package actuator

import (
	"context"
	"strings"
	"testing"
)

func TestBoolFuncApplyAndObserve(t *testing.T) {
	a := NewBoolFunc("hello-world", func(context.Context, ApplyRequest) (bool, string, error) {
		return true, "hello world delivered", nil
	})
	if err := a.Apply(context.Background(), ApplyRequest{Version: "v1"}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	converged, err := a.IsConverged(context.Background(), nil, "v1", "")
	if err != nil {
		t.Fatalf("IsConverged() error = %v", err)
	}
	if !converged {
		t.Fatal("IsConverged() = false, want true")
	}
	caps := a.Capabilities()
	if caps.SubstrateKind != "hello-world" || !caps.SupportsApply || !caps.SupportsObserve || caps.SupportsRollback {
		t.Fatalf("Capabilities() = %#v", caps)
	}
}

func TestBoolFuncFalseFailsApply(t *testing.T) {
	a := NewBoolFunc("hello-world", func(context.Context, ApplyRequest) (bool, string, error) {
		return false, "not today", nil
	})
	err := a.Apply(context.Background(), ApplyRequest{})
	if err == nil || !strings.Contains(err.Error(), "not today") {
		t.Fatalf("Apply() error = %v, want not today", err)
	}
}
