// Cluster CRD and supporting types: cluster capabilities, health,
// delivery loop status, heartbeat tracking, and bootstrap state.
package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Cluster shared types --------------------------------------------
// registered workload cluster. Written by kapro-cluster-controller at bootstrap
// time and refreshed on each heartbeat.
//
// Platform engineers and plan authors can reference these fields in stage
// selectors for cloud-aware, GPU-aware, and compliance-aware delivery waves.
//
// Example stage selector:
//
//	stageSelector:
//	  matchLabels:
//	    kapro.io/cloud: gcp
//	    kapro.io/region: europe-west1
type ClusterCapabilities struct {
	// ---- Software versions ----

	// K8sVersion is the Kubernetes server version (e.g. "1.30.2").
	// +optional
	K8sVersion string `json:"k8sVersion,omitempty"`
	// FluxVersion is the Flux version installed on this cluster (e.g. "2.3.0").
	// Empty when Flux is not installed.
	// +optional
	FluxVersion string `json:"fluxVersion,omitempty"`
	// ArgoCDVersion is the ArgoCD version installed on this cluster (e.g. "2.11.0").
	// Empty when ArgoCD is not installed.
	// +optional
	ArgoCDVersion string `json:"argoCDVersion,omitempty"`
	// SveltosVersion is the Sveltos version installed on this cluster.
	// Empty when Sveltos is not installed.
	// +optional
	SveltosVersion string `json:"sveltosVersion,omitempty"`

	// ---- Infrastructure metadata ----

	// NodeCount is the total number of nodes in the cluster at registration time.
	// +optional
	NodeCount int `json:"nodeCount,omitempty"`

	// Cloud identifies the cloud provider hosting this cluster.
	// Well-known values: gcp, aws, azure, digitalocean, stackit, on-prem.
	// Written by kapro-cluster-controller based on IMDS detection.
	// +optional
	Cloud string `json:"cloud,omitempty"`

	// Region is the cloud region of this cluster (e.g. europe-west1, us-east-1, westeurope).
	// +optional
	Region string `json:"region,omitempty"`

	// Zone is the cloud availability zone of the primary node pool
	// (e.g. europe-west1-b, us-east-1a, 1). Empty for regional clusters.
	// +optional
	Zone string `json:"zone,omitempty"`

	// AccountID is the cloud account or project identifier.
	// GCP: project ID. AWS: account ID. Azure: subscription UUID.
	// DigitalOcean: team ID. StackIT: project UUID.
	// Used for cost attribution, audit, and cross-account routing.
	// +optional
	AccountID string `json:"accountID,omitempty"`

	// ClusterID is the cloud-provider-assigned cluster identifier.
	// GCP: cluster resource name. AWS: cluster ARN. Azure: resource ID.
	// DigitalOcean: cluster UUID. StackIT: cluster UUID.
	// +optional
	ClusterID string `json:"clusterID,omitempty"`
}

// ClusterHealth aggregates workload health from the local delivery system.
type ClusterHealth struct {
	AllWorkloadsReady bool   `json:"allWorkloadsReady,omitempty"`
	ReadyWorkloads    int    `json:"readyWorkloads,omitempty"`
	FailedWorkloads   int    `json:"failedWorkloads,omitempty"`
	TotalWorkloads    int    `json:"totalWorkloads,omitempty"`
	Message           string `json:"message,omitempty"`
}

// DeliveryPhase classifies the spoke-side delivery loop's progress for one app.
// Pending → Pulling → Staging → Applying → Converged is the happy path; Failed
// is a sticky terminal state cleared on the next observed-digest change.
// +kubebuilder:validation:Enum=Pending;Pulling;Staging;Applying;Converged;Failed;Skipped
type DeliveryPhase string

const (
	// DeliveryPhasePending means the desired version is recorded but the
	// delivery loop has not yet processed it (e.g. cluster suspended,
	// rate-limited, or just received the change).
	DeliveryPhasePending DeliveryPhase = "Pending"
	// DeliveryPhasePulling means the OCI artifact is being fetched from the
	// registry.
	DeliveryPhasePulling DeliveryPhase = "Pulling"
	// DeliveryPhaseStaging means objects have been parsed and a dry-run
	// server-side apply is in progress. No live objects have been mutated.
	DeliveryPhaseStaging DeliveryPhase = "Staging"
	// DeliveryPhaseApplying means the staged objects validated and are now
	// being committed via server-side apply.
	DeliveryPhaseApplying DeliveryPhase = "Applying"
	// DeliveryPhaseConverged means every staged object committed successfully
	// and the app reports healthy.
	DeliveryPhaseConverged DeliveryPhase = "Converged"
	// DeliveryPhaseFailed is a terminal sticky state; cleared on the next
	// observed-digest change (i.e. when a new desired version arrives).
	DeliveryPhaseFailed DeliveryPhase = "Failed"
	// DeliveryPhaseSkipped means delivery was skipped intentionally — usually
	// because the Cluster is suspended.
	DeliveryPhaseSkipped DeliveryPhase = "Skipped"
)

// ClusterDeliveryStatus is the per-app delivery progress reported by the
// spoke-side cluster-controller. Written by exactly one writer: the spoke's
// own delivery loop on its own Cluster (RBAC-locked via resourceNames).
type ClusterDeliveryStatus struct {
	// Phase is the current phase of the delivery loop for this app.
	// +optional
	Phase DeliveryPhase `json:"phase,omitempty"`
	// DesiredVersion is the version the loop is targeting (mirrors
	// spec.desiredVersions[app] at the time of the last reconcile).
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`
	// ObservedDigest is the digest (e.g. "sha256:abcd…") of the OCI artifact
	// that produced the most recent successful apply. Stable across pulls of
	// the same tag, so a digest-equal short-circuit can skip re-apply when
	// the upstream tag is moved to point at the same content.
	// +optional
	ObservedDigest string `json:"observedDigest,omitempty"`
	// LastAppliedAt is when the last successful commit happened.
	// +optional
	LastAppliedAt *metav1.Time `json:"lastAppliedAt,omitempty"`
	// LastAttemptedAt is when the delivery loop most recently attempted this
	// app (success or failure). Useful for "is the spoke alive?" checks even
	// when ObservedDigest is empty.
	// +optional
	LastAttemptedAt *metav1.Time `json:"lastAttemptedAt,omitempty"`
	// LastError carries the human-readable error from the most recent failed
	// attempt. Cleared on success. Bounded to 4 KiB by the spoke before
	// writing to keep the status object small.
	// +optional
	LastError string `json:"lastError,omitempty"`
	// Staging records the latest staged apply diagnostics for providers that
	// support server-side dry-run before commit.
	// +optional
	Staging *DeliveryStagingStatus `json:"staging,omitempty"`
	// AppliedObjects is the count of distinct GVK+namespace+name tuples
	// committed by the most recent successful apply. Useful for "did this
	// just regress to applying 1 object instead of 47?" diagnostics.
	// +optional
	AppliedObjects int32 `json:"appliedObjects,omitempty"`
	// Format records the artifact format the spoke detected for the most
	// recent successful pull: "helm", "kustomize", or "raw-yaml". Empty until
	// the first pull succeeds.
	// +optional
	Format string `json:"format,omitempty"`
}

// ClusterPhase represents the convergence state of a workload cluster.
// +kubebuilder:validation:Enum=Pending;Applying;Converging;Converged;Failed;Unreachable
type ClusterPhase string

const (
	ClusterPhasePending     ClusterPhase = "Pending"
	ClusterPhaseApplying    ClusterPhase = "Applying"
	ClusterPhaseConverging  ClusterPhase = "Converging"
	ClusterPhaseConverged   ClusterPhase = "Converged"
	ClusterPhaseFailed      ClusterPhase = "Failed"
	ClusterPhaseUnreachable ClusterPhase = "Unreachable"
)

// ---- Cluster ----------------------------------------------------------
//
// Cluster is the cluster-inventory CRD for Fleet. One object per physical
// cluster in the fleet.
//
// Ownership split:
//   - spec (except desiredVersion/desiredAppKey): written by the platform team
//   - spec.desiredVersion, spec.desiredAppKey: written by the kapro-operator (PromotionRunReconciler)
//   - status: written by the cluster-controller (kapro-cluster-controller on the spoke)
//   - status.bootstrap: written by the hub csrapproval controller during registration

// ClusterSpec defines the desired state of a cluster in the Fleet fleet.
type ClusterSpec struct {
	// Delivery configures the substrate-neutral delivery adapter for this cluster.
	Delivery DeliverySpec `json:"delivery"`

	// HealthCheck configures active health polling for this cluster.
	// +optional
	HealthCheck *HealthCheckSpec `json:"healthCheck,omitempty"`

	// Topology holds hardware and scheduling metadata used by Plan stage selectors.
	// +optional
	Topology *TargetTopology `json:"topology,omitempty"`

	// DesiredVersion is written by the kapro-operator (PromotionRunReconciler).
	// The cluster-controller polls this field and patches the local delivery system.
	// Deprecated: use DesiredVersions map for multi-artifact promotionruns.
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`

	// DesiredAppKey is the key the cluster-controller uses when writing
	// status.currentVersions. Defaults to "default".
	// Deprecated: use DesiredVersions map for multi-artifact promotionruns.
	// +optional
	DesiredAppKey string `json:"desiredAppKey,omitempty"`

	// DesiredVersions is a map of appKey → version written by the kapro-operator.
	// The cluster-controller iterates this map and patches local delivery for each changed entry.
	// This replaces the single DesiredVersion/DesiredAppKey pair for multi-artifact promotionruns.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`

	// Suspend pauses all reconciliation for this cluster.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// ConsecutiveFailureThreshold is the number of consecutive heartbeat
	// misses required before the Cluster Ready condition flips to False
	// and the Phase transitions to Unreachable. Defaults to 3 to absorb
	// transient network blips without flapping. Pattern adopted from Sveltos
	// SveltosCluster.spec.consecutiveFailureThreshold.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	ConsecutiveFailureThreshold *int32 `json:"consecutiveFailureThreshold,omitempty"`

	// Bootstrap configures one-time cluster self-registration.
	//
	// Protocol (SA-token-mediated CSR; built-in K8s signer; works across any
	// cloud / any cluster / any K8s distribution):
	//
	//  1. Platform team creates this Cluster with spec.bootstrap set.
	//     `tokenHash` (when supplied) is an opaque slot identifier — its
	//     value isn't cryptographically verified by the hub today (the
	//     bootstrap ServiceAccount identity is the effective auth factor).
	//     Changing it alone does not reset bootstrap state in this release;
	//     delete and recreate the Cluster to issue a fresh bootstrap slot.
	//
	//  2. The hub Cluster bootstrap reconciler provisions a per-cluster
	//     ServiceAccount `kapro-bootstrap-<cluster>` in kapro-system with a
	//     narrowly-scoped ClusterRole (CSR-create only, for this signer
	//     name). It issues a TokenRequest for that SA (default 1h TTL,
	//     default kube-apiserver audience) and writes the rendered kubeconfig
	//     into Secret `kapro-bootstrap-kubeconfig-<cluster>`. The Secret
	//     name is recorded in `status.bootstrap.issuedBootstrapKubeconfig`.
	//
	//  3. The platform team ships that Secret out-of-band to the spoke
	//     cluster (Helm chart mount, kubectl image copy, GitOps, etc.).
	//     The kapro-cluster-controller pod on the spoke mounts the Secret,
	//     generates an ECDSA keypair, and submits a CertificateSigningRequest
	//     to the hub apiserver. The CSR carries:
	//       signerName = kubernetes.io/kube-apiserver-client
	//       Subject.CN = "kapro-cluster:<cluster>"
	//       Subject.O  = "kapro:cluster-controllers"
	//       Usages     = [client auth]
	//     The CSR's `spec.username` is automatically set by the apiserver
	//     to the bootstrap SA's identity
	//     ("system:serviceaccount:kapro-system:kapro-bootstrap-<cluster>").
	//
	//  4. The hub approver validates: (a) signer/CN/O/usages exactly match
	//     above; (b) Username equals the SA we provisioned for this
	//     Cluster — preventing a leaked Secret for cluster A from
	//     registering cluster B; (c) spec.bootstrap.expiresAt is in the
	//     future and status.bootstrap.used is false. It then approves the
	//     CSR via UpdateApproval. The K8s kube-controller-manager signs the
	//     cert with the apiserver's own client CA.
	//
	//  5. On approval the hub also creates a long-lived per-cluster
	//     ClusterRole and ClusterRoleBinding for the issued cert identity
	//     (User CN=`kapro-cluster:<cluster>`). Both names are
	//     `kapro:cluster-controller:<cluster>`. resourceNames lock the
	//     access scope to THIS cluster's Cluster + its own heartbeat
	//     Lease — a compromised spoke cannot patch a sibling.
	//
	//  6. The spoke uses the signed client cert for steady-state K8s API
	//     calls and rotates it via a renewal CSR before expiry (Username
	//     becomes the cluster cert identity rather than the bootstrap SA;
	//     the approver recognises the renewal class and skips the bootstrap
	//     slot check).
	//
	// RE-BOOTSTRAP: delete and recreate the Cluster to issue a fresh
	// bootstrap slot. Mutating spec.bootstrap.tokenHash alone is recorded
	// as desired state but does not reset status.bootstrap in this release.
	// +optional
	Bootstrap *ClusterBootstrapSpec `json:"bootstrap,omitempty"`

	// Provider declares how the hub discovers this cluster and reaches it.
	// The kind is an open provider name. Well-known built-ins include
	// outbound-agent, gcp-fleet, gcp-connect-gateway, eks, aks-arc, rhacm,
	// capi, and kubeconfig. Parameters are provider-specific and opaque to
	// Fleet core (StorageClass-style).
	//
	// When unset, the hub falls back to the legacy behaviour of looking up
	// a Secret-referenced kubeconfig from cluster annotations — kept for
	// backwards compatibility while existing Cluster objects migrate.
	// +optional
	Provider *ClusterProvider `json:"provider,omitempty"`
}

// ClusterProvider identifies the connectivity & identity model for a
// cluster. See projects/kapro/specs/fleet-and-oci-delivery-core-spec §3.2.
type ClusterProvider struct {
	// Kind selects a registered provider implementation. The value space is
	// intentionally open so platform teams can add discovery/auth providers
	// without Kapro adding enum values.
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,62}$`
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`
	// Parameters are opaque, provider-specific key/value settings. Fleet
	// core does not interpret them.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ClusterBootstrapSpec holds the one-time registration slot for a
// Cluster. See ClusterSpec.Bootstrap doc for the full protocol.
type ClusterBootstrapSpec struct {
	// TokenHash is an opaque, platform-supplied bootstrap-slot identifier in
	// SHA-256-hex shape (exactly 64 lowercase hex chars). It is NOT a
	// pre-image-of-token check today: the hub controller's effective
	// authorization is the bootstrap ServiceAccount it provisions (see the
	// ClusterSpec.Bootstrap protocol). Changing this value is recorded in spec
	// but does not reset status.bootstrap in this release.
	// Validation pattern remains a SHA-256 hex so existing tooling that
	// pre-computes the hash keeps working unmodified.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +optional
	TokenHash string `json:"tokenHash,omitempty"`

	// ExpiresAt is the absolute UTC time after which this bootstrap slot is invalid.
	// Set explicitly by the platform team for auditability.
	// If empty and TTL is set, the Cluster controller computes it on first reconcile.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// TTL is a convenience duration (e.g. "24h") used when ExpiresAt is not set explicitly.
	// The Cluster controller writes spec.bootstrap.expiresAt from
	// metadata.creationTimestamp + TTL at creation time and leaves it immutable.
	// +optional
	TTL string `json:"ttl,omitempty"`

	// Labels are applied to bootstrap resources created during registration
	// (ServiceAccount, kubeconfig Secret). Not used for stage selection — use
	// Cluster.metadata.labels for that.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// MaterialSource selects where the hub publishes the short-lived bootstrap
	// kubeconfig material. When omitted, the existing Kubernetes Secret path is
	// used. Vault is a preview contract and is rejected fail-closed by the
	// built-in controller unless explicitly implemented by platform automation.
	// +optional
	MaterialSource *ClusterBootstrapMaterialSource `json:"materialSource,omitempty"`
}

// ClusterBootstrapMaterialSourceType names the publication target for
// short-lived CSR bootstrap material.
// +kubebuilder:validation:Enum=KubernetesSecret;Vault
type ClusterBootstrapMaterialSourceType string

const (
	// ClusterBootstrapMaterialKubernetesSecret preserves the existing behavior:
	// the hub writes the rendered bootstrap kubeconfig to a Kubernetes Secret.
	ClusterBootstrapMaterialKubernetesSecret ClusterBootstrapMaterialSourceType = "KubernetesSecret"
	// ClusterBootstrapMaterialVault declares that bootstrap material should be
	// published through a Vault path instead of a Kubernetes Secret. The built-in
	// controller records this intent but fails closed unless an external
	// material publisher implements the Vault path.
	ClusterBootstrapMaterialVault ClusterBootstrapMaterialSourceType = "Vault"
)

// ClusterBootstrapMaterialSource configures where CSR bootstrap material is
// published.
// +kubebuilder:validation:XValidation:rule="(!has(self.type) || self.type == 'KubernetesSecret') ? !has(self.vault) : has(self.vault)",message="vault must be set only when type=Vault"
type ClusterBootstrapMaterialSource struct {
	// Type selects the material publication target.
	// +kubebuilder:default=KubernetesSecret
	// +optional
	Type ClusterBootstrapMaterialSourceType `json:"type,omitempty"`

	// Vault describes the external Vault location to publish or read bootstrap
	// kubeconfig material from. This is a preview API contract; the built-in
	// controller does not publish Vault material.
	// +optional
	Vault *VaultBootstrapMaterialSource `json:"vault,omitempty"`
}

// VaultBootstrapMaterialSource describes a Vault location for bootstrap
// kubeconfig material. It intentionally carries location metadata only; Vault
// auth is expected to come from the operator's runtime identity or an external
// platform integration, not from credentials embedded in the Cluster object.
type VaultBootstrapMaterialSource struct {
	// Address is the Vault server URL. When empty, platform automation may use
	// its runtime default.
	// +optional
	Address string `json:"address,omitempty"`

	// Mount is the Vault secrets engine mount, for example "secret" or "kv".
	// +optional
	Mount string `json:"mount,omitempty"`

	// Path is the Vault path for this cluster's bootstrap material.
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// KubeconfigField is the field name containing the rendered kubeconfig.
	// Defaults to "kubeconfig" for external automation.
	// +optional
	KubeconfigField string `json:"kubeconfigField,omitempty"`
}

// ClusterStatus is the observed state — written by cluster-controller and hub.
type ClusterStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ClusterPhase       `json:"phase,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`

	// Version is the primary deployed version (first entry in CurrentVersions).
	// Shown in kubectl/k9s printcolumns for quick fleet overview.
	// +optional
	Version string `json:"version,omitempty"`

	// Provider identifies how this cluster is managed (e.g. "gcp-fleet", "kubeconfig").
	// +optional
	Provider string `json:"provider,omitempty"`

	// CurrentVersions maps app keys to deployed versions. Written by cluster-controller.
	// +optional
	CurrentVersions map[string]string `json:"currentVersions,omitempty"`

	// DeliverySystem is the delivery system detected by the cluster-controller (e.g. "flux").
	// +optional
	DeliverySystem string `json:"deliverySystem,omitempty"`

	// Health aggregates workload health. Written by cluster-controller.
	// +optional
	Health ClusterHealth `json:"health,omitempty"`

	// Delivery tracks the spoke-side delivery loop's progress for each app.
	// Written by the cluster-controller's delivery loop (OCI Delivery Core).
	// Distinct from CurrentVersions: that map records the last successfully
	// committed version per app, while Delivery exposes in-flight phase,
	// observed digest, and last error for human and Decision API consumption.
	// +optional
	Delivery map[string]ClusterDeliveryStatus `json:"delivery,omitempty"`

	// ActivePromotionRun is the PromotionRun currently being processed for this cluster.
	// +optional
	ActivePromotionRun string `json:"activePromotionRun,omitempty"`

	// LastHeartbeat is the RFC3339 timestamp of the last cluster-controller heartbeat.
	// Deprecated: the authoritative heartbeat source is now the coordination.k8s.io/v1
	// Lease "kapro-heartbeat-<cluster>" in kapro-system. This field is still written
	// for backward compatibility but should not be relied upon for freshness checks.
	// +optional
	LastHeartbeat string `json:"lastHeartbeat,omitempty"`

	// ControllerVersion is the kapro-cluster-controller version running on this cluster.
	// +optional
	ControllerVersion string `json:"controllerVersion,omitempty"`

	// Capabilities is the self-reported capability profile written at registration time.
	// +optional
	Capabilities ClusterCapabilities `json:"capabilities,omitempty"`

	// Bootstrap tracks the one-time registration state. Written by the hub.
	// +optional
	Bootstrap *ClusterBootstrapStatus `json:"bootstrap,omitempty"`

	// Heartbeat tracks cluster reachability based on the spoke's coordination
	// Lease (`kapro-heartbeat-<cluster>` in kapro-system). Written exclusively
	// by the Cluster heartbeat reconciler. The reconciler is the single
	// writer of both this substruct AND the ConditionTypeReady condition; it
	// does NOT write Phase (kapro_controller reads conditions[Ready] and
	// computes Phase=Unreachable when Ready=False reason=Unreachable).
	// +optional
	Heartbeat *ClusterHeartbeatStatus `json:"heartbeat,omitempty"`
}

// ClusterHeartbeatStatus surfaces *why* Ready is what it is. Operators
// debugging a stuck cluster should be able to answer "how many misses, since
// when, what reason" from `kubectl get cluster -o yaml` alone.
type ClusterHeartbeatStatus struct {
	// ObservedAt is when the reconciler last computed reachability. Always
	// recent (≤ one reconcile interval). Distinct from LeaseObservedAt, which
	// is the spoke's last renewal.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`

	// LeaseObservedAt is the timestamp the reconciler extracted from the Lease
	// (spec.renewTime, or acquireTime, or metadata.creationTimestamp). Empty
	// when no Lease exists yet.
	// +optional
	LeaseObservedAt *metav1.Time `json:"leaseObservedAt,omitempty"`

	// ConsecutiveMisses is the count of consecutive reconciles where the Lease
	// was missing or stale. Reset to 0 on the first fresh observation. Compared
	// to Spec.ConsecutiveFailureThreshold to decide whether Ready=Unknown
	// (below threshold, transient) or Ready=False (at threshold, Unreachable).
	// +kubebuilder:validation:Minimum=0
	// +optional
	ConsecutiveMisses int32 `json:"consecutiveMisses,omitempty"`

	// LastTransitionAt is when the Ready condition last flipped. Distinct from
	// the condition's own LastTransitionTime so this substruct is self-contained
	// for dashboards that don't unpack Conditions.
	// +optional
	LastTransitionAt *metav1.Time `json:"lastTransitionAt,omitempty"`

	// Reason mirrors the current Ready condition reason for quick read access.
	// One of: HeartbeatFresh, HeartbeatStale, Unreachable, Suspended,
	// PushModeNoHeartbeat, NotRegistered.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ClusterBootstrapStatus tracks the one-time bootstrap registration state.
// Written by the hub Cluster bootstrap reconciler — first when it provisions
// the bootstrap SA + kubeconfig Secret, then again when a matching CSR is
// approved.
type ClusterBootstrapStatus struct {
	// Used is true once a matching CSR has been approved and the per-cluster
	// long-lived RBAC has been created. Enforces one-bootstrap-slot-per-
	// Cluster: a second CSR matching this slot but with a different
	// BoundCSRName is denied as a replay attempt.
	Used bool `json:"used,omitempty"`

	// UsedAt is when the bootstrap slot was consumed (the first matching CSR
	// approved).
	// +optional
	UsedAt *metav1.Time `json:"usedAt,omitempty"`

	// IssuedCredentialFor is the cluster name the bootstrap credential was
	// issued for — mirrors metadata.name on a successful registration. Serves
	// as a defensive cross-check when the hub controller later loads RBAC by
	// deterministic name.
	// +optional
	IssuedCredentialFor string `json:"issuedCredentialFor,omitempty"`

	// IssuedBootstrapKubeconfig is the name of the kubeconfig Secret in
	// kapro-system (`kapro-bootstrap-kubeconfig-<cluster>`) that the hub
	// controller provisioned for the spoke to mount. It is populated on every
	// (re-)provisioning pass and is the spoke's input for its first CSR
	// submission. The Secret carries a TokenRequest-issued bearer token whose
	// expiry is recorded in the Secret annotation
	// `kapro.io/bootstrap-expires-at`; the hub re-issues the Secret when the
	// token nears expiry while spec.bootstrap.expiresAt is still in the future.
	// +optional
	IssuedBootstrapKubeconfig string `json:"issuedBootstrapKubeconfig,omitempty"`

	// BoundCSRName is the CSR that consumed this bootstrap slot. Enables
	// idempotent retry: if status patching crashes after the slot is marked
	// Used but before UpdateApproval succeeds, the next reconcile recognises
	// the same CSR via this field and re-runs the approve step instead of
	// rejecting it as a replay.
	// +optional
	BoundCSRName string `json:"boundCSRName,omitempty"`

	// IssuedClusterRole is the name of the per-cluster long-lived ClusterRole
	// the bootstrap reconciler created (deterministic shape
	// `kapro:cluster-controller:<cluster>`). Recording it makes deletion
	// cascade trivial without re-derivation.
	// +optional
	IssuedClusterRole string `json:"issuedClusterRole,omitempty"`

	// IssuedClusterRoleBinding is the name of the per-cluster long-lived
	// ClusterRoleBinding (same `kapro:cluster-controller:<cluster>` shape as
	// IssuedClusterRole). Same rationale.
	// +optional
	IssuedClusterRoleBinding string `json:"issuedClusterRoleBinding,omitempty"`
}

// IsHeartbeatFresh returns true when the cluster last reported a heartbeat
// within the given timeout window.
func (s *ClusterStatus) IsHeartbeatFresh(timeout time.Duration) bool {
	if s.LastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s.LastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < timeout
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cl,categories=kapro-all
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Registered",type=string,JSONPath=`.status.conditions[?(@.type=="Registered")].status`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Healthy",type=boolean,JSONPath=`.status.health.allWorkloadsReady`
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.status.activePromotionRun`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.status.capabilities.region`,priority=1
// +kubebuilder:printcolumn:name="Cloud",type=string,JSONPath=`.status.capabilities.cloud`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Cluster represents one physical cluster in the Fleet fleet.
// It merges delivery config, fleet registration state, and the embedded
// one-time CSR bootstrap credential slot into a single resource.
//
// Labels on Cluster drive Plan stage selection (tier, region, wave, cloud, etc.).
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterSpec   `json:"spec,omitempty"`
	Status            ClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}
