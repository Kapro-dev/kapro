// Package conformance provides KSI (Kapro Standard Interface) conformance test suites.
//
// Any implementation of KGI (Gate), KAI (Actuator), or KCI (Connector) can be
// validated against these suites to prove it satisfies the contract required
// by the Kapro promotion engine.
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
// distributed as PluginRegistration plugins.
package conformance
