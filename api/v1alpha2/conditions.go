// Cross-cutting constants: finalizers, standard condition types, and the
// reason codes shared across Fleet controllers.
package v1alpha2

// Finalizer constants — prevents premature deletion of stateful resources.
const (
	// PromotionRunFinalizer is added to PromotionRun objects to allow cleanup of owned rollout state.
	PromotionRunFinalizer = "kapro.io/promotionrun-finalizer"
	// ClusterBootstrapFinalizer is reserved for embedded Cluster bootstrap cleanup.
	ClusterBootstrapFinalizer = "kapro.io/bootstrap-token-finalizer" //nolint:gosec // not a credential
	// BootstrapTokenFinalizer is kept for source compatibility with pre-v0.4.20
	// SDK users. There is no standalone BootstrapToken API; bootstrap state is
	// embedded in Cluster.spec.bootstrap and Cluster.status.bootstrap.
	//
	// Deprecated: use ClusterBootstrapFinalizer.
	BootstrapTokenFinalizer = ClusterBootstrapFinalizer
	// ClusterFinalizer is added to Cluster objects to allow bootstrap RBAC cleanup on deletion.
	ClusterFinalizer = "kapro.io/member-cluster-finalizer" //nolint:gosec // not a credential
)

// Condition type constants — Flux three-condition framework for operator status reporting.
const (
	// ConditionTypeReconciling indicates the controller is actively working on the object.
	// True while progressing, False when the object is terminal or suspended.
	ConditionTypeReconciling = "Reconciling"
	// ConditionTypeStalled indicates the object cannot progress without external intervention.
	// True when stuck (e.g. missing artifact, gate failure), False when healthy or recovering.
	ConditionTypeStalled = "Stalled"
	// ConditionTypeCompatible indicates a plugin reports a supported extension contract version.
	ConditionTypeCompatible = "Compatible"
	// ConditionTypeReady is the standard Kubernetes summary condition: True means the
	// object is observed-ready by its primary writer. For Cluster, this is True
	// after successful bootstrap registration AND a fresh heartbeat within the
	// configured staleness window. Surfaced in kubectl printcolumns.
	ConditionTypeReady = "Ready"
	// ConditionTypeRegistered indicates a Cluster has consumed its bootstrap
	// slot via a valid CSR exchange, and the hub has issued the per-cluster
	// ClusterRole + ClusterRoleBinding. True after first successful registration;
	// once True it stays True until the Cluster is deleted or its bootstrap
	// slot is rotated.
	ConditionTypeRegistered = "Registered"
)

// Reason codes attached to Cluster ConditionTypeReady by the
// Cluster heartbeat reconciler. Kept as exported constants so consumers
// (decision API, promotion controller, dashboards) can compare without
// string-literal drift.
const (
	// ReasonHeartbeatFresh — Lease was renewed within the freshness window.
	// Ready=True.
	ReasonHeartbeatFresh = "HeartbeatFresh"
	// ReasonHeartbeatStale — heartbeat is past the freshness window but the
	// per-cluster ConsecutiveFailureThreshold has not been reached yet.
	// Ready=Unknown; the cluster MAY still recover on next heartbeat.
	ReasonHeartbeatStale = "HeartbeatStale"
	// ReasonUnreachable — heartbeat has been stale for at least
	// Spec.ConsecutiveFailureThreshold consecutive reconciles. Ready=False;
	// Phase=Unreachable. Promotion targeting this cluster must defer (not fail).
	ReasonUnreachable = "Unreachable"
	// ReasonSuspended — Spec.Suspend=true. Heartbeat is intentionally ignored;
	// Ready=Unknown until Suspend clears.
	ReasonSuspended = "Suspended"
	// ReasonPushModeNoHeartbeat — Delivery.Mode=push, so there is no spoke
	// agent and no Lease to read. Ready=True (heartbeat is not applicable).
	ReasonPushModeNoHeartbeat = "PushModeNoHeartbeat"
	// ReasonNotRegistered — Cluster has never registered (bootstrap slot
	// not yet consumed). Heartbeat tracking is suspended until registration
	// completes; Ready stays Unknown so observers know the difference between
	// "registered but unreachable" and "not yet registered."
	ReasonNotRegistered = "NotRegistered"
)
