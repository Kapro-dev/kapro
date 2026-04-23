// Package provider provides the KCI (Kapro Cluster Interface) conformance suite.
package provider

import (
	"context"
	"testing"
	"time"


	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kci "kapro.io/kapro/pkg/provider"
)

// RunSuite runs the full KCI conformance suite against the provided Connector.
//
//	func TestConformance(t *testing.T) { provider.RunSuite(t, &MyConnector{}) }
func RunSuite(t *testing.T, c kci.Connector) {
	t.Helper()
	t.Run("KCI/NotNil", func(t *testing.T) { testNotNil(t, c) })
	t.Run("KCI/ContextCancellation", func(t *testing.T) { testContextCancellation(t, c) })
	t.Run("KCI/NilCluster", func(t *testing.T) { testNilCluster(t, c) })
	t.Run("KCI/IsReachableReturnsBool", func(t *testing.T) { testIsReachableReturnsBool(t, c) })
	t.Run("KCI/ConnectReturnsCfgOrError", func(t *testing.T) { testConnectReturnsCfgOrError(t, c) })
	t.Run("KCI/ConcurrentSafe", func(t *testing.T) { testConcurrentSafe(t, c) })
}

func testNotNil(t *testing.T, c kci.Connector) {
	t.Helper()
	if c == nil {
		t.Fatal("Connector implementation must not be nil")
	}
}

// testContextCancellation verifies Connect respects context cancellation.
func testContextCancellation(t *testing.T, c kci.Connector) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Connect(ctx, minimalCluster()) //nolint:errcheck
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Connector.Connect did not respect context cancellation within 2s")
	}
}

// testNilCluster verifies Connect/IsReachable handle nil without panicking.
func testNilCluster(t *testing.T, c kci.Connector) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Connector panicked on nil MemberCluster: %v", r)
		}
	}()
	c.Connect(context.Background(), nil)     //nolint:errcheck
	c.IsReachable(context.Background(), nil) //nolint:errcheck
}

// testIsReachableReturnsBool verifies IsReachable returns without panicking.
func testIsReachableReturnsBool(t *testing.T, c kci.Connector) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Connector.IsReachable panicked: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.IsReachable(ctx, minimalCluster())
}

// testConnectReturnsCfgOrError verifies Connect either returns a non-nil config
// or a descriptive error — never (nil, nil).
func testConnectReturnsCfgOrError(t *testing.T, c kci.Connector) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := c.Connect(ctx, minimalCluster())
	if cfg == nil && err == nil {
		t.Error("Connector.Connect returned (nil, nil) — must return either a config or an error")
	}
}

// testConcurrentSafe verifies the connector can be called concurrently.
func testConcurrentSafe(t *testing.T, c kci.Connector) {
	t.Helper()
	const goroutines = 10
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c.IsReachable(ctx, minimalCluster()) //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for i := 0; i < goroutines; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("concurrent IsReachable goroutine did not complete within 10s")
			return
		}
	}
}

func minimalCluster() *kaprov1alpha1.MemberCluster {
	mc := &kaprov1alpha1.MemberCluster{}
	mc.Name = "conformance-cluster"
	if mc.Spec.Provider == nil {
		mc.Spec.Provider = &kaprov1alpha1.ProviderSpec{}
	}
	return mc
}
