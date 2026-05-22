package gate

import (
	"context"
	"testing"
	"time"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestFuncEvaluate(t *testing.T) {
	g := Func(func(_ context.Context, req Request) (Result, error) {
		if req.Target != "dev" {
			t.Fatalf("target = %q", req.Target)
		}
		return MakePassed("ok"), nil
	})
	got, err := g.Evaluate(context.Background(), Request{Target: "dev"})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if got.Phase != kaprov1alpha2.GatePhasePassed || got.Message != "ok" {
		t.Fatalf("result = %#v", got)
	}
}

func TestConstructors(t *testing.T) {
	failed := MakeFailed("TooHigh", "%.2f > %.2f", 2.0, 1.0)
	if failed.Phase != kaprov1alpha2.GatePhaseFailed || failed.Reason != "TooHigh" || failed.Message != "2.00 > 1.00" {
		t.Fatalf("failed result = %#v", failed)
	}

	pending := MakePending("Wait", time.Now().Add(time.Second))
	if pending.Phase != kaprov1alpha2.GatePhasePending || pending.Reason != "Wait" || pending.RetryAfter == "" {
		t.Fatalf("pending result = %#v", pending)
	}
}

func TestRecoverConvertsPanicToFailedResult(t *testing.T) {
	g := Recover(Func(func(context.Context, Request) (Result, error) {
		panic("boom")
	}))
	got, err := g.Evaluate(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if got.Phase != kaprov1alpha2.GatePhaseFailed || got.Reason != "PanicRecovered" {
		t.Fatalf("result = %#v", got)
	}
}
