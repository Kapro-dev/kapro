package gate

import (
	"context"
	"testing"
)

func TestLegacyRegistryUpsertSignature(t *testing.T) {
	reg := NewRegistryWithoutTracing()
	first := Func(func(context.Context, Request) (Result, error) {
		return MakePassed("first"), nil
	})
	second := Func(func(context.Context, Request) (Result, error) {
		return MakePassed("second"), nil
	})

	if old := reg.Upsert("legacy", first); old != nil {
		t.Fatalf("first Upsert old = %#v, want nil", old)
	}
	old := reg.Upsert("legacy", second)
	if old == nil {
		t.Fatalf("second Upsert old = nil, want previous predicate")
	}
}
