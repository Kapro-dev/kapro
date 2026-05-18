// Package statistics provides the statistical primitives used by metric gate
// analysis modes (sequential, baseline, score, changePoint). It is a small
// math-only package with no Kubernetes or HTTP dependencies — kept separate
// from internal/gate so it can be exercised in isolation and reused by future
// gate kinds.
//
// Consumers: internal/gate (MetricsGate analysis modes).
package statistics
