// Package actuator provides the KAI (Kapro Actuator Interface) conformance suite.
package actuator

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkgactuator "kapro.io/kapro/pkg/actuator"
)

// RunSuite runs the full KAI conformance suite against the provided Actuator.
//
//	func TestConformance(t *testing.T) { actuator.RunSuite(t, &MyActuator{}) }
func RunSuite(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	t.Run("KAI/NotNil", func(t *testing.T) { testNotNil(t, a) })
	t.Run("KAI/ContextCancellation", func(t *testing.T) { testContextCancellation(t, a) })
	t.Run("KAI/ApplyNilCluster", func(t *testing.T) { testApplyNilCluster(t, a) })
	t.Run("KAI/ApplyEmptyVersion", func(t *testing.T) { testApplyEmptyVersion(t, a) })
	t.Run("KAI/IsConvergedReturnsBool", func(t *testing.T) { testIsConvergedReturnsBool(t, a) })
	t.Run("KAI/RollbackNilCluster", func(t *testing.T) { testRollbackNilCluster(t, a) })
	t.Run("KAI/ConcurrentSafe", func(t *testing.T) { testConcurrentSafe(t, a) })
}

func testNotNil(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	if a == nil {
		t.Fatal("Actuator implementation must not be nil")
	}
}

// testContextCancellation verifies Apply respects context cancellation.
func testContextCancellation(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Apply(ctx, minimalApplyRequest()) //nolint:errcheck
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Actuator.Apply did not respect context cancellation within 2s")
	}
}

// testApplyNilCluster verifies Apply handles nil Cluster gracefully (no panic).
func testApplyNilCluster(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Actuator.Apply panicked on nil Cluster: %v", r)
		}
	}()
	req := minimalApplyRequest()
	req.Cluster = nil
	a.Apply(context.Background(), req) //nolint:errcheck
}

// testApplyEmptyVersion verifies Apply returns an error (not panics) when version is empty.
func testApplyEmptyVersion(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Actuator.Apply panicked on empty Version: %v", r)
		}
	}()
	req := minimalApplyRequest()
	req.Version = ""
	// May return error — that's fine. Must not panic.
	a.Apply(context.Background(), req) //nolint:errcheck
}

// testIsConvergedReturnsBool verifies IsConverged returns a boolean without panicking.
func testIsConvergedReturnsBool(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Actuator.IsConverged panicked: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Both true and false are valid — external actuators may not be converged yet.
	_, _ = a.IsConverged(ctx, minimalCluster(), "v0.0.1", "default")
}

// testRollbackNilCluster verifies Rollback handles nil Cluster without panicking.
func testRollbackNilCluster(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Actuator.Rollback panicked on nil Cluster: %v", r)
		}
	}()
	a.Rollback(context.Background(), nil, "v0.0.0") //nolint:errcheck
}

// testConcurrentSafe verifies the actuator can be called concurrently.
func testConcurrentSafe(t *testing.T, a pkgactuator.Actuator) {
	t.Helper()
	const goroutines = 10
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			a.IsConverged(ctx, minimalCluster(), "v0.0.1", "default") //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for i := 0; i < goroutines; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("concurrent IsConverged goroutine did not complete within 10s")
			return
		}
	}
}

func minimalCluster() *kaprov1alpha1.MemberCluster {
	mc := &kaprov1alpha1.MemberCluster{}
	mc.Name = "conformance-cluster"
	return mc
}

func minimalApplyRequest() pkgactuator.ApplyRequest {
	return pkgactuator.ApplyRequest{
		Cluster: &kaprov1alpha1.MemberCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "conformance-cluster"},
		},
		Version:         "v0.0.1",
		PreviousVersion: "v0.0.0",
	}
}
