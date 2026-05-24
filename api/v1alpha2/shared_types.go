// Shared cross-domain types: cluster topology, delivery mode/driver, and
// the DeliverySpec consumed by Cluster, Fleet, and Backend.
package v1alpha2

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

// DeliveryMode controls where backend delivery is executed.
// +kubebuilder:validation:Enum=push;pull
type DeliveryMode string

const (
	// DeliveryModePush means the hub calls a hub-side backend adapter.
	DeliveryModePush DeliveryMode = "push"
	// DeliveryModePull means the hub records desired state and a spoke agent
	// calls a local backend adapter.
	DeliveryModePull DeliveryMode = "pull"
)

// BackendRuntime identifies where a backend adapter is allowed to run.
// +kubebuilder:validation:Enum=Hub;Spoke;Both
type BackendRuntime string

const (
	BackendRuntimeHub   BackendRuntime = "Hub"
	BackendRuntimeSpoke BackendRuntime = "Spoke"
	BackendRuntimeBoth  BackendRuntime = "Both"
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

// BackendDriver identifies the backend implementation family.
// +kubebuilder:validation:Enum=flux;argo;oci;external
type BackendDriver string

const (
	BackendDriverFlux BackendDriver = "flux"
	BackendDriverArgo BackendDriver = "argo"
	// BackendDriverOCI is the built-in spoke-side OCI Delivery Core: the
	// kapro-cluster-controller pulls OCI artifacts (Helm chart, raw YAML
	// tarball, or Kustomize tarball) and server-side applies them via the
	// two-phase staging engine. Available out of the box; requires no Flux,
	// Argo, or Sveltos installation on the spoke.
	BackendDriverOCI      BackendDriver = "oci"
	BackendDriverExternal BackendDriver = "external"
)

// OpenSubstrateKindPattern documents the DNS-style validation used for open
// substrate and provider names. Kubebuilder markers carry the actual CRD rule.
const OpenSubstrateKindPattern = `^[a-z][a-z0-9-]{0,62}$`

// BackendSubstrateSpec identifies a delivery domain and the actuator that
// implements it. Kind is intentionally open: built-ins such as argo, flux, oci,
// and webhook are documented well-known values, while platform teams can
// register their own kinds such as hello-world or company-paas.
type BackendSubstrateSpec struct {
	// Kind is the open delivery domain/category.
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,62}$`
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`
	// Actuator names the concrete Kapro actuator/plugin implementation. When
	// empty, Kapro derives the default actuator for well-known built-ins from
	// kind; custom substrates normally set this explicitly.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,62}$`
	// +kubebuilder:validation:MaxLength=63
	Actuator string `json:"actuator,omitempty"`
}

// BackendExecutionSpec selects the topology for this backend profile.
type BackendExecutionSpec struct {
	// Mode identifies where and how delivery is invoked.
	// +kubebuilder:default="hub-push"
	Mode ExecutionMode `json:"mode"`
}

// DeliverySpec selects a backend-neutral delivery profile for a cluster or fleet.
// Backend-specific resource names live in parameters and are interpreted only by
// the selected backend adapter.
type DeliverySpec struct {
	// Mode controls who performs delivery.
	// +kubebuilder:default="pull"
	Mode DeliveryMode `json:"mode"`
	// BackendRef is the Backend name. Built-in profiles commonly use
	// "flux" or "argo"; external profiles may use any platform-defined name.
	BackendRef string `json:"backendRef"`
	// Staging declares optional pre-commit safety semantics for backends that
	// support staging. When omitted, existing backend defaults are preserved.
	// The built-in OCI pull backend already uses TwoPhase/Abort behavior.
	// +optional
	Staging *DeliveryStagingSpec `json:"staging,omitempty"`
	// Parameters are opaque backend-specific settings, following the
	// StorageClass.parameters pattern. Fleet core does not interpret them.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// RegistryKey returns the composite key used to resolve the delivery adapter.
func (d *DeliverySpec) RegistryKey() string {
	if d == nil {
		return "/"
	}
	return string(d.Mode) + "/" + d.BackendRef
}

// Param returns a backend-specific delivery parameter with a default.
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
