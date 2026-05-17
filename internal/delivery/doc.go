// Package delivery implements the spoke-side OCI Delivery Core: the pieces
// of kapro-cluster-controller that pull an OCI artifact from a registry,
// detect its format, render it into a list of Kubernetes objects, and
// apply those objects to the local spoke cluster using a two-phase
// server-side apply (Sveltos pullmode pattern, re-implemented cleanly).
//
// Layering (top-to-bottom):
//
//	Delivery.Reconcile(ctx, app, ArtifactRef)
//	   │
//	   ├── Pull          — oras-go fetches the OCI artifact into a fs.FS
//	   ├── Detect        — Chart.yaml → helm, kustomization.yaml → kustomize, else raw-yaml
//	   ├── Render        — handler-specific: raw-yaml | helm | kustomize → []object
//	   └── Apply         — two-phase engine: dry-run server-side apply all,
//	                       then commit all if every dry-run succeeded; if any
//	                       dry-run fails, none commit.
//
// The package is consumed by cmd/kapro-cluster-controller (spoke binary)
// where a goroutine watches FleetCluster.spec.desiredVersions, calls
// Delivery.Reconcile per (app, version), and patches
// FleetCluster.status.delivery + FleetCluster.status.currentVersions with
// the result.
//
// Test surface:
//   - Pull / Detect / Render / Apply each individually unit-testable.
//   - Two-phase engine driven by a fake client.Client; assertions over
//     "no live objects mutated when dry-run fails".
//   - End-to-end test pushes a tiny raw-yaml artifact to an in-test OCI
//     registry (oras-go memory store + httptest server) and asserts
//     FleetCluster.status.delivery transitions through the expected phases.
package delivery
