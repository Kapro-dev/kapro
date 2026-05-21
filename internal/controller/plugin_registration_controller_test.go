package controller

import (
	"context"
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/plugin/probe"

	"github.com/prometheus/client_golang/prometheus/testutil"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestPluginRegistrationReconcilerSetsReadyStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reg := &kaprov1alpha2.Plugin{
		ObjectMeta: metav1.ObjectMeta{Name: "test-plugin"},
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeActuator,
			Name:     "test",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(reg).WithStatusSubresource(&kaprov1alpha2.Plugin{}).Build()
	r := &PluginRegistrationReconciler{
		Client:   c,
		Recorder: record.NewFakeRecorder(8),
		Prober: fakePluginProber{result: probe.Result{
			Ready:           true,
			Reason:          "ProbeSucceeded",
			Message:         "ok",
			Version:         "v1",
			ContractVersion: probe.ContractVersion(),
			Capabilities:    []string{"apply"},
		}},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: objectKey(reg.Name)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got kaprov1alpha2.Plugin
	if err := c.Get(context.Background(), objectKey(reg.Name), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Status.Ready {
		t.Fatal("status.ready = false")
	}
	if got.Status.Version != "v1" {
		t.Fatalf("status.version = %q", got.Status.Version)
	}
	if got.Status.ContractVersion != probe.ContractVersion() {
		t.Fatalf("status.contractVersion = %q", got.Status.ContractVersion)
	}
	if len(got.Status.Capabilities) != 1 || got.Status.Capabilities[0] != "apply" {
		t.Fatalf("status.capabilities = %v", got.Status.Capabilities)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v", ready)
	}
	compatible := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeCompatible)
	if compatible == nil || compatible.Status != metav1.ConditionTrue {
		t.Fatalf("Compatible condition = %#v", compatible)
	}
	if !controllerutil.ContainsFinalizer(&got, pluginRegistrationMetricsFinalizer) {
		t.Fatalf("finalizers = %v, want %q", got.Finalizers, pluginRegistrationMetricsFinalizer)
	}
}

func TestPluginRegistrationReconcilerSetsStalledStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reg := &kaprov1alpha2.Plugin{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-plugin"},
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeGate,
			Name:     "bad",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(reg).WithStatusSubresource(&kaprov1alpha2.Plugin{}).Build()
	r := &PluginRegistrationReconciler{
		Client:   c,
		Recorder: record.NewFakeRecorder(8),
		Prober: fakePluginProber{result: probe.Result{
			Ready:   false,
			Reason:  "DialFailed",
			Message: "connection refused",
		}},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: objectKey(reg.Name)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got kaprov1alpha2.Plugin
	if err := c.Get(context.Background(), objectKey(reg.Name), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status.Ready {
		t.Fatal("status.ready = true")
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "DialFailed" {
		t.Fatalf("Ready condition = %#v", ready)
	}
	stalled := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	if stalled == nil || stalled.Status != metav1.ConditionTrue {
		t.Fatalf("Stalled condition = %#v", stalled)
	}
	compatible := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeCompatible)
	if compatible == nil || compatible.Status != metav1.ConditionUnknown {
		t.Fatalf("Compatible condition = %#v", compatible)
	}
}

func TestPluginRegistrationReconcilerSetsIncompatibleStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reg := &kaprov1alpha2.Plugin{
		ObjectMeta: metav1.ObjectMeta{Name: "newer-plugin"},
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeGate,
			Name:     "newer",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(reg).WithStatusSubresource(&kaprov1alpha2.Plugin{}).Build()
	r := &PluginRegistrationReconciler{
		Client:   c,
		Recorder: record.NewFakeRecorder(8),
		Prober: fakePluginProber{result: probe.Result{
			Ready:           false,
			Reason:          "UnsupportedContractVersion",
			Message:         "plugin reported unsupported KGI contract version \"v2\"; supported versions: v1alpha1",
			ContractVersion: "v2",
		}},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: objectKey(reg.Name)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got kaprov1alpha2.Plugin
	if err := c.Get(context.Background(), objectKey(reg.Name), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status.Ready {
		t.Fatal("status.ready = true")
	}
	if got.Status.ContractVersion != "v2" {
		t.Fatalf("status.contractVersion = %q", got.Status.ContractVersion)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "UnsupportedContractVersion" {
		t.Fatalf("Ready condition = %#v", ready)
	}
	compatible := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeCompatible)
	if compatible == nil || compatible.Status != metav1.ConditionFalse || compatible.Reason != "UnsupportedContractVersion" {
		t.Fatalf("Compatible condition = %#v", compatible)
	}
}

func TestPluginRegistrationReconcilerDeletesReadinessMetricOnDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reg := &kaprov1alpha2.Plugin{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "slo-gate",
			Finalizers: []string{pluginRegistrationMetricsFinalizer},
		},
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeGate,
			Name:     "slo/gate",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(reg).WithStatusSubresource(&kaprov1alpha2.Plugin{}).Build()
	r := &PluginRegistrationReconciler{Client: c, Recorder: record.NewFakeRecorder(8)}

	readiness := kaprometrics.PluginProbeReady.WithLabelValues("gate", "slo/gate")
	readiness.Set(1)
	if err := c.Delete(context.Background(), reg); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: objectKey(reg.Name)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := testutil.ToFloat64(readiness); got != 0 {
		t.Fatalf("probe readiness gauge = %v, want deleted/zero", got)
	}
}

type fakePluginProber struct {
	result probe.Result
}

func (f fakePluginProber) Probe(context.Context, kaprov1alpha2.Plugin) probe.Result {
	return f.result
}

func objectKey(name string) types.NamespacedName {
	return types.NamespacedName{Name: name}
}
