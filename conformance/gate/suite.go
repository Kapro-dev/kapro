// Package gate provides the KGI (Kapro Gate Interface) conformance test suite.
// Any gate.Gate implementation must pass RunSuite to be considered KGI-conformant.
package gate

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

// RunSuite runs the full KGI conformance suite against the provided Gate implementation.
// Call this from your plugin's test file:
//
//	func TestConformance(t *testing.T) { gate.RunSuite(t, &MyGate{}) }
func RunSuite(t *testing.T, g pkggate.Gate) {
	t.Helper()
	t.Run("KGI/NotNil", func(t *testing.T) { testNotNil(t, g) })
	t.Run("KGI/ContextCancellation", func(t *testing.T) { testContextCancellation(t, g) })
	t.Run("KGI/NilSync", func(t *testing.T) { testNilPromotion(t, g) })
	t.Run("KGI/NilPolicy", func(t *testing.T) { testNilPolicy(t, g) })
	t.Run("KGI/ValidRequest", func(t *testing.T) { testValidRequest(t, g) })
	t.Run("KGI/ResultShape", func(t *testing.T) { testResultShape(t, g) })
	t.Run("KGI/ConcurrentSafe", func(t *testing.T) { testConcurrentSafe(t, g) })
}

// testNotNil verifies the implementation is not nil itself (guard).
func testNotNil(t *testing.T, g pkggate.Gate) {
	t.Helper()
	if g == nil {
		t.Fatal("Gate implementation must not be nil")
	}
}

// testContextCancellation verifies the gate respects context cancellation
// and returns promptly (within 2s) rather than blocking forever.
func testContextCancellation(t *testing.T, g pkggate.Gate) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Evaluate(ctx, minimalRequest()) //nolint:errcheck
	}()

	select {
	case <-done:
		// passed — returned before deadline + grace window
	case <-time.After(2 * time.Second):
		t.Error("Gate.Evaluate did not respect context cancellation within 2s")
	}
}

// testNilPromotion verifies the gate handles a nil Sync without panicking.
func testNilPromotion(t *testing.T, g pkggate.Gate) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Gate.Evaluate panicked on nil Sync: %v", r)
		}
	}()
	req := minimalRequest()
	req.Sync = nil
	g.Evaluate(context.Background(), req) //nolint:errcheck
}

// testNilPolicy verifies the gate handles a nil Policy without panicking.
func testNilPolicy(t *testing.T, g pkggate.Gate) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Gate.Evaluate panicked on nil Policy: %v", r)
		}
	}()
	req := minimalRequest()
	req.Policy = nil
	g.Evaluate(context.Background(), req) //nolint:errcheck
}

// testValidRequest verifies the gate returns a result (pass or fail — both valid)
// without error when given a well-formed request. Implementations that require
// external services may return an error here — that is acceptable.
func testValidRequest(t *testing.T, g pkggate.Gate) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := g.Evaluate(ctx, minimalRequest())
	if err != nil {
		// Error is acceptable (e.g., external service unavailable in unit test).
		// What we're verifying: it doesn't panic and returns a typed Result.
		t.Logf("Gate.Evaluate returned error (acceptable in unit context): %v", err)
		return
	}
	// Result must have a non-empty message when the gate didn't pass — helps operator debugging.
	if !result.IsPassed() && result.Message == "" {
		t.Error("Gate.Evaluate returned a non-Passed Phase with empty Message — must explain why")
	}
}

// testResultShape verifies the Result fields have sensible values.
func testResultShape(t *testing.T, g pkggate.Gate) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := g.Evaluate(ctx, minimalRequest())
	if err != nil {
		return // external service errors don't invalidate shape rules
	}

	// RetryAfter must be a valid Go duration string or empty.
	if result.RetryAfter != "" {
		if _, parseErr := time.ParseDuration(result.RetryAfter); parseErr != nil {
			t.Errorf("Gate.Result.RetryAfter %q is not a valid duration: %v", result.RetryAfter, parseErr)
		}
	}
}

// testConcurrentSafe verifies the gate can be called concurrently without data races.
// Run with -race to detect issues.
func testConcurrentSafe(t *testing.T, g pkggate.Gate) {
	t.Helper()
	const goroutines = 10
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			g.Evaluate(ctx, minimalRequest()) //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for i := 0; i < goroutines; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("concurrent Evaluate goroutine did not complete within 10s")
			return
		}
	}
}

// minimalRequest returns a well-formed Request with all required fields set.
func minimalRequest() pkggate.Request {
	return pkggate.Request{
		Sync: &kaprov1alpha1.Sync{
			ObjectMeta: metav1.ObjectMeta{Name: "conformance-test-sync"},
			Spec: kaprov1alpha1.SyncSpec{
				ReleaseRef:     "conformance-release",
				EnvironmentRef: "conformance-env",
				Version:        "v0.0.1",
				Gate: &kaprov1alpha1.GatePolicySpec{
					Mode: kaprov1alpha1.GateModeAuto,
					Gate: kaprov1alpha1.GateSpec{
						Metrics: []kaprov1alpha1.MetricGate{
							{Provider: "conformance", Query: "up", Window: "5m"},
						},
					},
				},
			},
		},
		Policy: &kaprov1alpha1.GatePolicySpec{
			Mode: kaprov1alpha1.GateModeAuto,
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{
					{Provider: "conformance", Query: "up", Window: "5m"},
				},
			},
		},
		MetricIndex: 0,
	}
}
