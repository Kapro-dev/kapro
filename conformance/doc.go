// Package conformance provides Kapro extension-interface conformance test suites.
//
// Kapro exposes two pluggable extension interfaces today:
//
//   - Gate (pkg/gate) — evaluators that answer "is it safe to advance?"
//   - Actuator (pkg/actuator) — drivers that apply a version to a target cluster.
//
// Any implementation of Gate or Actuator can be validated against these
// suites to prove it satisfies the contract required by the Kapro
// promotion engine.
//
// Usage in a plugin's test file:
//
//	func TestGateConformance(t *testing.T) {
//	    gate.RunSuite(t, &MyGateImplementation{...})
//	}
//
//	func TestActuatorConformance(t *testing.T) {
//	    actuator.RunSuite(t, &MyActuatorImplementation{...})
//	}
//
// The suites use only the standard testing package — no framework dependency.
// Implementations that pass the suite are Kapro-compatible and can be
// wired into the operator at startup via actuator.Registry or gate.Registry.
package conformance
