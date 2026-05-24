// Source CRD: native promotion unit catalog Fleet can move through
// the fleet (greenfield Helm units or discovered Argo/Flux objects).
package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Source ---------------------------------------------------------------

// SourceSpec defines the native promotion units Fleet can move
// through a fleet. Units may map to generated Flux resources in greenfield mode
// or to substrate-native objects discovered from Argo/Flux in native mode.
// Used inline by Fleet.spec.source or referenced by Fleet.spec.sourceRef.
type SourceSpec struct {
	// SubstrateRef is the Substrate this source is normally discovered from
	// or packaged for. Fleet uses it as metadata; delivery still comes from
	// Fleet.spec.delivery and Cluster.spec.delivery.
	// +optional
	SubstrateRef string `json:"substrateRef,omitempty"`
	// Registries defines HelmRepository sources for generated Flux resources.
	// +optional
	Registries []SourceRegistry `json:"registries,omitempty"`
	// Units defines the native deployable units Fleet promotes.
	// +kubebuilder:validation:MinItems=1
	Units []Unit `json:"units"`
	// Defaults are inherited by every unit unless overridden.
	// +optional
	Defaults *SourceDefaults `json:"defaults,omitempty"`
	// Overrides are per-cluster or per-label value patches layered on top of defaults.
	// +optional
	Overrides []SourceOverride `json:"overrides,omitempty"`
	// HelmReleaseNamespace is where HelmRelease CRs live on spokes (not the workloads).
	// +kubebuilder:default="flux-system"
	HelmReleaseNamespace string `json:"helmReleaseNamespace,omitempty"`
}

// SourceRegistry defines a Helm chart source. Generates a HelmRepository on spoke.
type SourceRegistry struct {
	// Name is the registry identifier referenced by units via repo field.
	Name string `json:"name"`
	// URL is the Helm repository URL (OCI or HTTPS).
	// Supports ${variable} substitution (e.g. oci://${gcpArtifactRegistry}/helm/ldl).
	URL string `json:"url"`
	// Type is "oci" (auto-detected for oci:// URLs) or "default" (HTTPS).
	// +optional
	Type string `json:"type,omitempty"`
	// Provider is the auth provider: "generic" (default), "gcp", "aws", "azure".
	// "gcp" uses Workload Identity — no credentials needed.
	// +kubebuilder:default="generic"
	// +optional
	Provider string `json:"provider,omitempty"`
	// Interval is how often to check for new chart versions.
	// +kubebuilder:default="5m"
	// +optional
	Interval string `json:"interval,omitempty"`
}

// SourceDefaults are inherited by every unit unless overridden at unit level.
type SourceDefaults struct {
	// Repo is the default registry name (from spec.registries).
	// +optional
	Repo string `json:"repo,omitempty"`
	// TargetNamespace is where workload pods run. Supports ${variable} substitution.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// Timeout for install and upgrade operations.
	// +kubebuilder:default="10m"
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Retries is the number of install/upgrade retry attempts.
	// +kubebuilder:default=3
	// +optional
	Retries int32 `json:"retries,omitempty"`
	// ValuesFrom references ConfigMaps/Secrets with Helm values applied to all units.
	// +optional
	ValuesFrom []ValuesReference `json:"valuesFrom,omitempty"`
	// Values are base Helm values applied to every unit (deep-merged with unit values).
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// Unit is one deployable unit within a Source.
// It can describe a generated Helm unit for greenfield scaffolds or an existing
// substrate-native object discovered from Argo/Flux.
type Unit struct {
	// Name is the stable Fleet unit identifier.
	Name string `json:"name"`
	// SubstrateKind identifies the substrate-native object kind when this unit maps
	// to an existing object, for example Application, ApplicationSet,
	// Kustomization, or HelmRelease.
	// +optional
	SubstrateKind string `json:"substrateKind,omitempty"`
	// Namespace is the substrate-native object namespace when applicable.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// VersionField is the substrate-native field Fleet changes for this unit,
	// for example spec.source.targetRevision for Argo CD Applications.
	// +optional
	VersionField string `json:"versionField,omitempty"`
	// SourcePath is the repo-relative file path Fleet updates for existing
	// GitOps promotion. It is required for file-backed units whose
	// VersionField does not already include a file path.
	// +optional
	SourcePath string `json:"sourcePath,omitempty"`
	// Version is the default chart/revision for the unit at install time. This is
	// the package/catalog default; the per-rollout target version comes from
	// Promotion.spec.version (or --version on `kapro promote`) and is copied
	// into the controller-owned PromotionRun.
	// Supports ${VARIABLE} substitution from cluster-vars.
	// +optional
	Version string `json:"version,omitempty"`
	// Repo references a registry from spec.registries by name. Inherits from defaults if empty.
	// +optional
	Repo string `json:"repo,omitempty"`
	// ChartName overrides the Helm chart name when different from unit name.
	// Example: unit "keycloak" uses chart "keycloakx".
	// +optional
	ChartName string `json:"chartName,omitempty"`
	// TargetNamespace is where workload pods run on spoke. Inherits from defaults if empty.
	// Supports ${variable} substitution.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// Wave controls deployment ordering (lower = earlier). Units in the same wave
	// deploy in parallel. Wave N waits for wave N-1 to be Ready.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Wave int32 `json:"wave,omitempty"`
	// DependsOn lists unit names that must be Ready before this one starts.
	// Creates HelmRelease-level dependsOn within the same wave.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// Values are inline Helm values. Deep-merged with defaults.values (unit wins on conflict).
	// +optional
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
	// ValuesFrom references ConfigMaps/Secrets with Helm values.
	// When set, REPLACES defaults.valuesFrom (not appended).
	// +optional
	ValuesFrom []ValuesReference `json:"valuesFrom,omitempty"`
	// Timeout for install and upgrade. Inherits from defaults if empty.
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Retries for install/upgrade remediation. Inherits from defaults if empty.
	// +optional
	Retries *int32 `json:"retries,omitempty"`
	// Prune controls whether Flux deletes resources when removed. Default: true.
	// Set to false for databases, Kafka, PVCs.
	// +optional
	Prune *bool `json:"prune,omitempty"`
	// CRDs controls CRD install policy: "Skip" (default), "Create", "CreateReplace".
	// +kubebuilder:validation:Enum=Skip;Create;CreateReplace
	// +optional
	CRDs string `json:"crds,omitempty"`
	// Suspend pauses reconciliation for this unit.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// ValuesReference references a ConfigMap or Secret for Helm values.
type ValuesReference struct {
	// Kind is "ConfigMap" (default) or "Secret".
	// +kubebuilder:default="ConfigMap"
	// +optional
	Kind string `json:"kind,omitempty"`
	// Name of the ConfigMap or Secret.
	Name string `json:"name"`
	// ValuesKey is the data key to use. Default: "values.yaml".
	// +optional
	ValuesKey string `json:"valuesKey,omitempty"`
	// Optional marks this values source as non-required.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

// SourceOverride patches Helm values for a subset of clusters.
type SourceOverride struct {
	// Selector matches clusters by labels. Applied to all matching clusters.
	// +optional
	Selector map[string]string `json:"selector,omitempty"`
	// Clusters is an explicit list of cluster names. Takes precedence over selector.
	// +optional
	Clusters []string `json:"clusters,omitempty"`
	// Unit restricts this override to a single unit. Empty means all.
	// +optional
	Unit string `json:"unit,omitempty"`
	// Values are Helm value patches merged on top of defaults.
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,shortName=src,categories=kapro-all
// +kubebuilder:printcolumn:name="Units",type=integer,JSONPath=`.metadata.annotations.kapro\.io/unit-count`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Source defines the units Fleet promotes. It is the source/app-unit
// contract for both generated greenfield layouts and native Argo/Flux layouts.
type Source struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SourceSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type SourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Source `json:"items"`
}
