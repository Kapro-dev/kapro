// Shared cross-domain types: cluster topology, delivery mode/driver, and
// the DeliverySpec consumed by Cluster, Fleet, and Substrate.
package v1alpha1

// ---- Shared cluster types ---------------------------------------------------

type TargetTopology struct {
	// Accelerator is the GPU/accelerator type in this cluster.
	// Well-known values: nvidia-h100, nvidia-a100, nvidia-l40s, amd-mi300x, tpu-v5e.
	// +optional
	Accelerator string `json:"accelerator,omitempty"`
	// GPUCount is the total number of GPU devices across all nodes in this cluster.
	// +optional
	// +kubebuilder:validation:Minimum=0
	GPUCount int32 `json:"gpuCount,omitempty"`
	// GPUMemoryGB is the memory per GPU device in gigabytes (e.g. 80 for H100 SXM).
	// +optional
	// +kubebuilder:validation:Minimum=0
	GPUMemoryGB int32 `json:"gpuMemoryGb,omitempty"`
	// NodeCount is the number of GPU nodes (not total devices).
	// +optional
	// +kubebuilder:validation:Minimum=0
	NodeCount int32 `json:"nodeCount,omitempty"`
	// Tier classifies the cluster's role in the delivery wave.
	// Well-known values: canary, shadow, prod.
	// +optional
	Tier string `json:"tier,omitempty"`
}

// DeliveryMode controls where substrate delivery is executed.
// +kubebuilder:validation:Enum=push;pull
type DeliveryMode string

const (
	// DeliveryModePush means the hub calls a hub-side substrate adapter.
	DeliveryModePush DeliveryMode = "push"
	// DeliveryModePull means the hub records desired state and a spoke agent
	// calls a local substrate adapter.
	DeliveryModePull DeliveryMode = "pull"
)

// SubstrateRuntime identifies where a substrate adapter is allowed to run.
// +kubebuilder:validation:Enum=Hub;Spoke;Both
type SubstrateRuntime string

const (
	SubstrateRuntimeHub   SubstrateRuntime = "Hub"
	SubstrateRuntimeSpoke SubstrateRuntime = "Spoke"
	SubstrateRuntimeBoth  SubstrateRuntime = "Both"
)

// ExecutionMode identifies where and how Kapro invokes delivery.
//
// +kubebuilder:validation:Enum=hub-push;spoke-pull;external-pull
type ExecutionMode string

const (
	// ExecutionModeHubPush means the Kapro hub invokes the actuator directly.
	ExecutionModeHubPush ExecutionMode = "hub-push"
	// ExecutionModeSpokePull means a cluster-side spoke pulls approved work and
	// invokes the actuator near the target cluster.
	ExecutionModeSpokePull ExecutionMode = "spoke-pull"
	// ExecutionModeExternalPull means an external platform or plugin pulls
	// approved Kapro decisions and reports status back.
	ExecutionModeExternalPull ExecutionMode = "external-pull"
)

// SubstrateDriver identifies the substrate implementation family.
// +kubebuilder:validation:Enum=flux;argo;oci;external
type SubstrateDriver string

const (
	SubstrateDriverFlux SubstrateDriver = "flux"
	SubstrateDriverArgo SubstrateDriver = "argo"
	// SubstrateDriverOCI is the built-in spoke-side OCI Delivery Core: the
	// kapro-cluster-controller pulls OCI artifacts (Helm chart, raw YAML
	// tarball, or Kustomize tarball) and server-side applies them via the
	// two-phase staging engine. Available out of the box; requires no Flux,
	// Argo, or Sveltos installation on the spoke.
	SubstrateDriverOCI      SubstrateDriver = "oci"
	SubstrateDriverExternal SubstrateDriver = "external"
)

// OpenSubstrateKindPattern documents the DNS-style validation used for open
// substrate and provider names. Kubebuilder markers carry the actual CRD rule.
const OpenSubstrateKindPattern = `^[a-z][a-z0-9-]{0,62}$`

// SubstrateImplementationSpec identifies a delivery domain and the actuator that
// implements it. Kind is intentionally open: built-ins such as argo, flux, oci,
// and kubernetes-apply are documented well-known values, while platform teams
// can register their own kinds such as hello-world or company-paas.
type SubstrateImplementationSpec struct {
	// Kind is the open delivery domain/category.
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,62}$`
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`
	// Actuator names the concrete Kapro actuator/plugin implementation. When
	// empty on an open-substrate manifest, Kapro falls back to kind.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,62}$`
	// +kubebuilder:validation:MaxLength=63
	Actuator string `json:"actuator,omitempty"`
}

// SubstrateExecutionSpec selects the topology for this substrate profile.
type SubstrateExecutionSpec struct {
	// Mode identifies where and how delivery is invoked.
	// +kubebuilder:default="hub-push"
	Mode ExecutionMode `json:"mode"`
}

// SubstrateClassReference names a cluster-scoped SubstrateClass.
type SubstrateClassReference struct {
	// Name is the SubstrateClass name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SubstrateObjectKindReference identifies a typed substrate-owned CRD kind
// without naming one concrete object instance.
type SubstrateObjectKindReference struct {
	// APIVersion is the referenced API version, for example
	// argocd.substrate.kapro.io/v1alpha1.
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`
	// Kind is the referenced Kubernetes kind, for example
	// ArgoCDSubstrateConfig.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
}

// SubstrateObjectReference points at a typed substrate-owned configuration
// object. Namespace is optional so cluster-scoped config resources and
// namespaced config resources can both use the same reference shape.
type SubstrateObjectReference struct {
	// APIVersion is the referenced API version, for example
	// argocd.substrate.kapro.io/v1alpha1.
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`
	// Kind is the referenced Kubernetes kind, for example
	// ArgoCDSubstrateConfig.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
	// Name is the referenced object name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Namespace is the referenced object namespace when the referenced kind is
	// namespaced. It is empty for cluster-scoped substrate config kinds.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// DeliverySpec selects a substrate-neutral delivery profile for a cluster or fleet.
// Substrate-specific resource names live in parameters and are interpreted only by
// the selected substrate adapter.
type DeliverySpec struct {
	// Mode controls who performs delivery.
	// +kubebuilder:default="pull"
	Mode DeliveryMode `json:"mode"`
	// SubstrateRef is the Substrate name. Built-in profiles commonly use
	// "flux" or "argo"; external profiles may use any platform-defined name.
	SubstrateRef string `json:"substrateRef"`
	// Staging declares optional pre-commit safety semantics for substrates that
	// support staging. When omitted, existing substrate defaults are preserved.
	// The built-in OCI pull substrate already uses TwoPhase/Abort behavior.
	// +optional
	Staging *DeliveryStagingSpec `json:"staging,omitempty"`
	// Parameters are opaque substrate-specific settings, following the
	// StorageClass.parameters pattern. Fleet core does not interpret them.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// RegistryKey returns the composite key used to resolve the delivery adapter.
func (d *DeliverySpec) RegistryKey() string {
	if d == nil {
		return "/"
	}
	return string(d.Mode) + "/" + d.SubstrateRef
}

// Param returns a substrate-specific delivery parameter with a default.
func (d *DeliverySpec) Param(name, fallback string) string {
	if d == nil || d.Parameters == nil || d.Parameters[name] == "" {
		return fallback
	}
	return d.Parameters[name]
}

type HealthCheckSpec struct {
	Endpoint string `json:"endpoint"`
	Interval string `json:"interval"`
}
