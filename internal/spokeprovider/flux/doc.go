// Package flux implements the spoke-side delivery Provider for
// SubstrateKindFlux. The provider OBSERVES local Flux state — it never
// mutates Flux CRs.
//
// Division of labour:
//   - HUB writes the desired version onto FleetCluster.spec.desiredVersions
//     and (for Flux substrates) the hub-side fluxoperator actuator patches
//     the Flux source's tag/version on the spoke. This is what causes
//     Flux to actually fetch the new artifact.
//   - SPOKE (this provider) reads the local OCIRepository (and optionally a
//     HelmRelease for per-app readiness) to compute whether Flux has
//     reached the desired state, and reports back via the spoke status
//     loop into FleetCluster.status.delivery[<appKey>].
//
// This split keeps Kapro a single-writer for desired state and Flux the
// single-writer for delivery execution, while giving the hub fleet-wide
// visibility into convergence without dialing into the spoke.
//
// Parameters consumed from ReconcileRequest.Parameters (merged from
// SubstrateProfile.spec.parameters and FleetCluster.spec.delivery.parameters,
// cluster wins):
//
//	ociRepositoryName        — name of the Flux OCIRepository on the spoke
//	                           (required)
//	ociRepositoryNamespace   — namespace of the OCIRepository
//	                           (defaults to "flux-system")
//	helmReleaseName          — optional: HelmRelease name driven by the
//	                           OCIRepository. When set, readiness is the AND
//	                           of OCIRepository.Ready and HelmRelease.Ready.
//	helmReleaseNamespace     — namespace of the HelmRelease
//	                           (defaults to the OCIRepository namespace)
//
// Phase mapping (DeliveryPhase):
//
//	OCIRepository missing                          → Failed (ConfigError)
//	OCIRepository.status.artifact unset            → Pulling
//	OCIRepository Ready=False                      → Failed
//	OCIRepository revision != DesiredVersion       → Pulling
//	OCIRepository revision == DesiredVersion       → Applying
//	+ HelmRelease Ready=True                       → Converged
//	(no HelmRelease configured) and revision match → Converged
//	FleetCluster.spec.suspend == true              → Skipped
package flux
