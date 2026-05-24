package controller

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	pullactuator "kapro.io/kapro/internal/actuator/pull"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
)

type recordingNotifier struct {
	events   []notification.Event
	policies []notification.NotificationPolicy
}

func (n *recordingNotifier) Notify(_ context.Context, event notification.Event, policy notification.NotificationPolicy) {
	n.events = append(n.events, event)
	n.policies = append(n.policies, policy)
}

type capabilityActuator struct {
	applyCount   int
	deltaCount   int
	observeCount int
}

func (a *capabilityActuator) Apply(context.Context, actuator.ApplyRequest) error {
	a.applyCount++
	return nil
}

func (a *capabilityActuator) IsConverged(context.Context, *kaprov1alpha2.Cluster, string, string) (bool, error) {
	return true, nil
}

func (a *capabilityActuator) Rollback(context.Context, *kaprov1alpha2.Cluster, string, string) error {
	return nil
}

func (a *capabilityActuator) ApplyDelta(context.Context, actuator.DeltaApplyRequest) (int, error) {
	a.deltaCount++
	return 1, nil
}

func (a *capabilityActuator) IsAllConverged(context.Context, *kaprov1alpha2.Cluster, map[string]string) (bool, error) {
	a.observeCount++
	return true, nil
}

func TestApplyDesiredVersionsHonorsDeltaCapability(t *testing.T) {
	cluster := &kaprov1alpha2.Cluster{
		Status: kaprov1alpha2.ClusterStatus{CurrentVersions: map[string]string{"api": "old"}},
	}
	desired := map[string]string{"api": "new", "worker": "new"}

	withDelta := &capabilityActuator{}
	if _, err := applyDesiredVersions(context.Background(), withDelta, actuator.Capabilities{SupportsApply: true, SupportsDelta: true}, cluster, desired); err != nil {
		t.Fatalf("apply with delta: %v", err)
	}
	if withDelta.deltaCount != 1 || withDelta.applyCount != 0 {
		t.Fatalf("with delta apply=%d delta=%d", withDelta.applyCount, withDelta.deltaCount)
	}

	withoutDelta := &capabilityActuator{}
	if _, err := applyDesiredVersions(context.Background(), withoutDelta, actuator.Capabilities{SupportsApply: true}, cluster, desired); err != nil {
		t.Fatalf("apply without delta: %v", err)
	}
	if withoutDelta.deltaCount != 0 || withoutDelta.applyCount != 2 {
		t.Fatalf("without delta apply=%d delta=%d", withoutDelta.applyCount, withoutDelta.deltaCount)
	}
}

func TestHandleApplyingSkipsConvergenceWhenObserveUnsupported(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{
				Mode:       kaprov1alpha2.DeliveryModePull,
				BackendRef: "flux",
			},
		},
		Status: kaprov1alpha2.ClusterStatus{
			CurrentVersions: map[string]string{"api": "old"},
			Conditions: []metav1.Condition{{
				Type:   kaprov1alpha2.ConditionTypeReady,
				Status: metav1.ConditionTrue,
				Reason: "HeartbeatFresh",
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		WithStatusSubresource(&kaprov1alpha2.Cluster{}).
		Build()

	act := &capabilityActuator{}
	reg := actuator.NewRegistry()
	if err := reg.RegisterRegistration(actuator.Registration{
		Name: "pull/flux",
		Capabilities: actuator.Capabilities{
			Adapter:       "flux",
			Modes:         []kaprov1alpha2.DeliveryMode{kaprov1alpha2.DeliveryModePull},
			SupportsApply: true,
		},
		Actuator: act,
	}); err != nil {
		t.Fatalf("register actuator: %v", err)
	}
	r := &TargetReconciler{
		Client:           c,
		Recorder:         record.NewFakeRecorder(4),
		ActuatorRegistry: reg,
	}
	promotionrun := &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "promo-1"}}
	target := &kaprov1alpha2.TargetExecutionState{
		PromotionRunRef: "promo-1",
		Target:          "cluster-a",
		PlanRef:         "wave-1",
		Stage:           "prod",
		AppKey:          "api",
		Version:         "new",
		DesiredVersions: map[string]string{"api": "new"},
	}

	result, err := r.handleApplying(ctx, promotionrun, target)
	if err != nil {
		t.Fatalf("handleApplying: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("result = %+v, want no requeue", result)
	}
	if target.Phase != kaprov1alpha2.TargetPhaseConverged {
		t.Fatalf("target phase = %q, want Converged", target.Phase)
	}
	if act.applyCount != 1 || act.observeCount != 0 {
		t.Fatalf("apply/observe counts = %d/%d, want 1/0", act.applyCount, act.observeCount)
	}
}

func TestHandleApplyingResolvesClassRefBackendToBuiltInActuator(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{
				Mode:       kaprov1alpha2.DeliveryModePush,
				BackendRef: "local-direct",
			},
		},
		Status: kaprov1alpha2.ClusterStatus{CurrentVersions: map[string]string{"api": "old"}},
	}
	backend := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "local-direct"},
		Spec: kaprov1alpha2.BackendSpec{
			ClassRef: &kaprov1alpha2.SubstrateClassReference{Name: "kubernetes-apply"},
			ConfigRef: &kaprov1alpha2.SubstrateObjectReference{
				APIVersion: "kubernetes.substrate.kapro.io/v1alpha1",
				Kind:       "KubernetesApplyConfig",
				Name:       "local-direct",
			},
		},
		Status: kaprov1alpha2.BackendStatus{Ready: true},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, backend).
		WithStatusSubresource(&kaprov1alpha2.Cluster{}).
		Build()

	act := &capabilityActuator{}
	reg := actuator.NewRegistry()
	if err := reg.RegisterRegistration(actuator.Registration{
		Name: "push/direct",
		Capabilities: actuator.Capabilities{
			Adapter:         "direct",
			Modes:           []kaprov1alpha2.DeliveryMode{kaprov1alpha2.DeliveryModePush},
			SupportsApply:   true,
			SupportsObserve: true,
			SupportsDelta:   true,
		},
		Actuator: act,
	}); err != nil {
		t.Fatalf("register actuator: %v", err)
	}
	r := &TargetReconciler{
		Client:           c,
		Recorder:         record.NewFakeRecorder(4),
		ActuatorRegistry: reg,
	}
	promotionrun := &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "promo-1"}}
	target := &kaprov1alpha2.TargetExecutionState{
		PromotionRunRef: "promo-1",
		Target:          "cluster-a",
		PlanRef:         "wave-1",
		Stage:           "prod",
		AppKey:          "api",
		Version:         "new",
		DesiredVersions: map[string]string{"api": "new"},
	}

	result, err := r.handleApplying(ctx, promotionrun, target)
	if err != nil {
		t.Fatalf("handleApplying: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("result = %+v, want no requeue", result)
	}
	if target.Phase != kaprov1alpha2.TargetPhaseConverged {
		t.Fatalf("target phase = %q, want Converged", target.Phase)
	}
	if act.deltaCount != 1 || act.observeCount != 1 {
		t.Fatalf("delta/observe counts = %d/%d, want 1/1", act.deltaCount, act.observeCount)
	}
}

func TestSyncPromotionTargetPhaseLabelPersistsMetadata(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-wave-cluster-a"},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseWaitingApproval},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(target).Build()
	r := &TargetReconciler{Client: c}

	if err := r.syncPromotionTargetPhaseLabel(ctx, target); err != nil {
		t.Fatal(err)
	}
	var got kaprov1alpha2.Target
	if err := c.Get(ctx, client.ObjectKey{Name: target.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels["kapro.io/phase"] != string(kaprov1alpha2.TargetPhaseWaitingApproval) {
		t.Fatalf("phase label = %q", got.Labels["kapro.io/phase"])
	}
}

func TestPromotionTargetReconcileSyncsTerminalPhaseLabel(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-wave-cluster-a"},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseFailed},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(target).Build()
	r := &TargetReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: target.Name}}); err != nil {
		t.Fatal(err)
	}
	var got kaprov1alpha2.Target
	if err := c.Get(ctx, client.ObjectKey{Name: target.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels["kapro.io/phase"] != string(kaprov1alpha2.TargetPhaseFailed) {
		t.Fatalf("phase label = %q", got.Labels["kapro.io/phase"])
	}
}

func TestPromotionTargetPredicates_RejectedStatusChangeEnqueues(t *testing.T) {
	p := promotionTargetPredicates()
	oldObj := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rel-wave-prod-cluster-a",
			Generation: 1,
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				Phase: kaprov1alpha2.TargetPhaseWaitingApproval,
			},
		},
	}
	newObj := oldObj.DeepCopy()
	newObj.Status.Rejected = true
	newObj.Status.RejectedBy = "alice"

	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected rejected status change to enqueue reconcile")
	}
}

func TestPromotionTargetFleetClusterPredicates_HeartbeatOnlyIgnored(t *testing.T) {
	p := promotionTargetFleetClusterPredicates()
	oldObj := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status: kaprov1alpha2.ClusterStatus{
			Phase:         kaprov1alpha2.ClusterPhaseConverged,
			LastHeartbeat: "2025-01-01T00:00:00Z",
		},
	}
	newObj := oldObj.DeepCopy()
	newObj.Status.LastHeartbeat = "2025-01-01T00:00:30Z"

	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected heartbeat-only FleetCluster update to be ignored")
	}
}

func TestHeartbeatLeasePredicates_IgnoreFreshRenewal(t *testing.T) {
	p := heartbeatLeasePredicates()
	oldRenew := metav1.NewMicroTime(time.Now().Add(-30 * time.Second).UTC())
	newRenew := metav1.NewMicroTime(time.Now().UTC())
	oldObj := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: heartbeatLeaseName("cluster-a")},
		Spec:       coordinationv1.LeaseSpec{RenewTime: &oldRenew},
	}
	newObj := oldObj.DeepCopy()
	newObj.Spec.RenewTime = &newRenew

	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected fresh-to-fresh heartbeat renewal to be ignored")
	}
}

func TestHeartbeatLeasePredicates_EnqueueOnFreshnessBoundary(t *testing.T) {
	p := heartbeatLeasePredicates()
	oldRenew := metav1.NewMicroTime(time.Now().Add(-heartbeatFreshTimeout - time.Second).UTC())
	newRenew := metav1.NewMicroTime(time.Now().UTC())
	oldObj := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: heartbeatLeaseName("cluster-a")},
		Spec:       coordinationv1.LeaseSpec{RenewTime: &oldRenew},
	}
	newObj := oldObj.DeepCopy()
	newObj.Spec.RenewTime = &newRenew

	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected stale-to-fresh heartbeat renewal to enqueue")
	}
}

func TestPromotionTargetReconcilePullOCIRecordsDesiredState(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-oci"},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "oci://registry.example.com/apps/checkout@sha256:222",
			Plans: []kaprov1alpha2.PlanRef{{
				Name: "default", Plan: "plan",
			}},
		},
	}
	cluster := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{
				Mode:       kaprov1alpha2.DeliveryModePull,
				BackendRef: "oci",
			},
		},
		Status: kaprov1alpha2.ClusterStatus{
			Conditions: []metav1.Condition{{
				Type:   kaprov1alpha2.ConditionTypeReady,
				Status: metav1.ConditionTrue,
				Reason: kaprov1alpha2.ReasonHeartbeatFresh,
			}},
			CurrentVersions: map[string]string{"default": "oci://registry.example.com/apps/checkout@sha256:111"},
			Health:          kaprov1alpha2.ClusterHealth{AllWorkloadsReady: true},
		},
	}
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-oci-default-cluster-a"},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: "rel-oci",
			Target:          "cluster-a",
			Plan:            "plan",
			PlanRef:         "default",
			Stage:           "prod",
			Version:         "oci://registry.example.com/apps/checkout@sha256:222",
			AppKey:          "default",
			DesiredVersions: map[string]string{
				"default": "oci://registry.example.com/apps/checkout@sha256:222",
			},
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				PromotionRunRef: "rel-oci",
				Target:          "cluster-a",
				Plan:            "plan",
				PlanRef:         "default",
				Stage:           "prod",
				Version:         "oci://registry.example.com/apps/checkout@sha256:222",
				AppKey:          "default",
				DesiredVersions: map[string]string{
					"default": "oci://registry.example.com/apps/checkout@sha256:222",
				},
				Phase: kaprov1alpha2.TargetPhaseApplying,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.Target{}, &kaprov1alpha2.Cluster{}).
		WithObjects(promotionrun, cluster, target).
		Build()
	actuators := actuator.NewRegistry()
	if err := actuators.Register("pull/oci", &pullactuator.PullActuator{HubClient: c}); err != nil {
		t.Fatal(err)
	}
	r := &TargetReconciler{
		Client:           c,
		ActuatorRegistry: actuators,
		GateRegistry:     gate.NewRegistry(),
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: target.Name}}); err != nil {
		t.Fatal(err)
	}

	var updatedCluster kaprov1alpha2.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: "cluster-a"}, &updatedCluster); err != nil {
		t.Fatal(err)
	}
	want := "oci://registry.example.com/apps/checkout@sha256:222"
	if updatedCluster.Spec.DesiredVersions["default"] != want {
		t.Fatalf("desiredVersions[default]=%q, want %q", updatedCluster.Spec.DesiredVersions["default"], want)
	}
	if updatedCluster.Spec.DesiredVersion != want || updatedCluster.Spec.DesiredAppKey != "default" {
		t.Fatalf("compat desired fields=%q/%q, want %q/default", updatedCluster.Spec.DesiredVersion, updatedCluster.Spec.DesiredAppKey, want)
	}
}

func TestUpdatePromotionTargetStatusContract_SetsObservedGenerationAndConditions(t *testing.T) {
	r := &TargetReconciler{}
	rt := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rel-wave-prod-cluster-a",
			Generation: 3,
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				Phase:   kaprov1alpha2.TargetPhaseConverged,
				Message: "done",
			},
		},
	}

	r.updatePromotionTargetStatusContract(rt)

	if rt.Status.ObservedGeneration != 3 {
		t.Fatalf("expected ObservedGeneration=3, got %d", rt.Status.ObservedGeneration)
	}
	ready := false
	for _, cond := range rt.Status.Conditions {
		if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		t.Fatal("expected Ready=True condition on converged target")
	}
}

func TestNotifyPersistedTransitions_OnlyOnPersistedPhaseChange(t *testing.T) {
	notifier := &recordingNotifier{}
	r := &TargetReconciler{Notifier: notifier}
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
	}
	prev := &kaprov1alpha2.TargetExecutionState{
		Target:  "cluster-a",
		Version: "repo@sha256:abc",
		Phase:   kaprov1alpha2.TargetPhasePending,
	}
	curr := prev.DeepCopy()
	curr.Phase = kaprov1alpha2.TargetPhaseHealthCheck

	r.notifyPersistedTransitions(context.Background(), promotionrun, prev, curr)

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 persisted phase notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Phase != string(kaprov1alpha2.TargetPhaseHealthCheck) {
		t.Fatalf("expected HealthCheck notification, got %q", notifier.events[0].Phase)
	}
}

func TestNotifyPersistedTransitions_ApprovalOnlyAfterPersistedStamp(t *testing.T) {
	notifier := &recordingNotifier{}
	r := &TargetReconciler{Notifier: notifier}
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
	}
	prev := &kaprov1alpha2.TargetExecutionState{
		Target:  "cluster-a",
		Version: "repo@sha256:abc",
		Phase:   kaprov1alpha2.TargetPhaseWaitingApproval,
	}
	curr := prev.DeepCopy()
	curr.ApprovalSentAt = "2025-01-01T00:00:00Z"

	r.notifyPersistedTransitions(context.Background(), promotionrun, prev, curr)

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 approval notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Phase != string(kaprov1alpha2.TargetPhaseWaitingApproval) {
		t.Fatalf("expected WaitingApproval notification, got %q", notifier.events[0].Phase)
	}
}

func TestNotifyGateEvent_SendsSemanticGateType(t *testing.T) {
	notifier := &recordingNotifier{}
	r := &TargetReconciler{Notifier: notifier}
	promotionrun := &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1"}}
	target := &kaprov1alpha2.TargetExecutionState{
		Target:  "cluster-a",
		Version: "repo@sha256:abc",
		PlanRef: "main",
		Stage:   "canary",
		Phase:   kaprov1alpha2.TargetPhaseMetricsCheck,
		Gate: &kaprov1alpha2.GatePolicySpec{
			Notifications: []kaprov1alpha2.NotificationSpec{{Type: "webhook", Events: []string{notification.EventGatePassed}}},
		},
	}

	r.notifyGateEvent(context.Background(), promotionrun, target, notification.EventGatePassed, "metrics", "passed", false)

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 gate notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Type != notification.EventGatePassed {
		t.Fatalf("expected gate passed event, got %q", notifier.events[0].Type)
	}
	if notifier.events[0].Plan != "main" || notifier.events[0].Stage != "canary" || notifier.events[0].Target != "cluster-a" {
		t.Fatalf("gate event context not populated: %#v", notifier.events[0])
	}
	if len(notifier.policies) != 1 || len(notifier.policies[0].Channels) != 1 {
		t.Fatalf("expected gate policy to provide one notification channel, got %#v", notifier.policies)
	}
}
