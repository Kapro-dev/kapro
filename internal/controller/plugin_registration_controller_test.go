package controller

import (
	"context"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/plugin/probe"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPluginRegistrationReconcilerSetsReadyStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reg := &kaprov1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "test-plugin"},
		Spec: kaprov1alpha1.PluginRegistrationSpec{
			Type:     kaprov1alpha1.PluginTypeActuator,
			Name:     "test",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
			Endpoint: "bufnet",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(reg).WithStatusSubresource(&kaprov1alpha1.PluginRegistration{}).Build()
	r := &PluginRegistrationReconciler{
		Client:   c,
		Recorder: record.NewFakeRecorder(8),
		Prober: fakePluginProber{result: probe.Result{
			Ready:        true,
			Reason:       "ProbeSucceeded",
			Message:      "ok",
			Version:      "v1",
			Capabilities: []string{"apply"},
		}},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: objectKey(reg.Name)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got kaprov1alpha1.PluginRegistration
	if err := c.Get(context.Background(), objectKey(reg.Name), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Status.Ready {
		t.Fatal("status.ready = false")
	}
	if got.Status.Version != "v1" {
		t.Fatalf("status.version = %q", got.Status.Version)
	}
	if len(got.Status.Capabilities) != 1 || got.Status.Capabilities[0] != "apply" {
		t.Fatalf("status.capabilities = %v", got.Status.Capabilities)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v", ready)
	}
}

func TestPluginRegistrationReconcilerCallsRuntimeReloader(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reg := &kaprov1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "runtime-plugin", Generation: 1},
		Spec: kaprov1alpha1.PluginRegistrationSpec{
			Type:     kaprov1alpha1.PluginTypeActuator,
			Name:     "runtime/test",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
			Endpoint: "bufnet",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(reg).WithStatusSubresource(&kaprov1alpha1.PluginRegistration{}).Build()
	reloader := &fakeRuntimeReloader{}
	r := &PluginRegistrationReconciler{
		Client:          c,
		Recorder:        record.NewFakeRecorder(8),
		RuntimeReloader: reloader,
		Prober: fakePluginProber{result: probe.Result{
			Ready:   true,
			Reason:  "ProbeSucceeded",
			Message: "ok",
		}},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: objectKey(reg.Name)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if reloader.reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", reloader.reconcileCalls)
	}
	if reloader.last.Name != reg.Name {
		t.Fatalf("runtime registration name = %q, want %q", reloader.last.Name, reg.Name)
	}
}

func TestPluginRegistrationReconcilerUnregistersMissingRuntimePlugin(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kaprov1alpha1.PluginRegistration{}).Build()
	reloader := &fakeRuntimeReloader{}
	r := &PluginRegistrationReconciler{
		Client:          c,
		Recorder:        record.NewFakeRecorder(8),
		RuntimeReloader: reloader,
	}
	key := objectKey("deleted-plugin")

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if reloader.unregisterCalls != 1 {
		t.Fatalf("unregister calls = %d, want 1", reloader.unregisterCalls)
	}
	if reloader.unregistered != key {
		t.Fatalf("unregistered = %v, want %v", reloader.unregistered, key)
	}
}

func TestPluginRegistrationReconcilerSetsStalledStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reg := &kaprov1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-plugin"},
		Spec: kaprov1alpha1.PluginRegistrationSpec{
			Type:     kaprov1alpha1.PluginTypeGate,
			Name:     "bad",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
			Endpoint: "bufnet",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(reg).WithStatusSubresource(&kaprov1alpha1.PluginRegistration{}).Build()
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

	var got kaprov1alpha1.PluginRegistration
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
	stalled := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if stalled == nil || stalled.Status != metav1.ConditionTrue {
		t.Fatalf("Stalled condition = %#v", stalled)
	}
}

type fakePluginProber struct {
	result probe.Result
}

func (f fakePluginProber) Probe(context.Context, kaprov1alpha1.PluginRegistration) probe.Result {
	return f.result
}

type fakeRuntimeReloader struct {
	reconcileCalls  int
	unregisterCalls int
	last            kaprov1alpha1.PluginRegistration
	unregistered    types.NamespacedName
}

func (f *fakeRuntimeReloader) Reconcile(_ context.Context, _ client.Reader, reg kaprov1alpha1.PluginRegistration) (bool, error) {
	f.reconcileCalls++
	f.last = reg
	return true, nil
}

func (f *fakeRuntimeReloader) Unregister(key types.NamespacedName) {
	f.unregisterCalls++
	f.unregistered = key
}

func objectKey(name string) types.NamespacedName {
	return types.NamespacedName{Name: name}
}
