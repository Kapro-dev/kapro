// Package kapro is the small public Go SDK for Kapro.
//
// The package is intentionally thin. Builders produce the same
// kapro.io/v1alpha2 Kubernetes API objects that users write as YAML, the
// subscriber consumes the same CloudEvents vocabulary published by pkg/events,
// and the gate interface gives custom gate authors a stable in-process shape
// without importing controller internals.
//
// # Versioning
//
// pkg/kapro follows Kapro's pre-stable release line. During v0.1.x the SDK
// targets kapro.io/v1alpha2 and may add new builder methods, event helpers, or
// optional fields in patch/minor releases. Existing exported names in this
// package are kept source-compatible within the v0.1.x line unless a security
// or correctness fix requires a documented break in the changelog.
//
// When Kapro graduates the Kubernetes API to a new served version, the SDK
// will either keep compatibility wrappers for the older shape or publish the
// break in a new release line with migration notes.
package kapro
