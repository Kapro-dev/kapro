package main

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestReadPackageSourceFromFleetInlineSource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	source := &kaprov1alpha1.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha1.FleetSpec{
			Source: &kaprov1alpha1.SourceSpec{
				Units: []kaprov1alpha1.Unit{{Name: "api", Version: "1.0.0"}},
			},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()

	got, err := readPackageSource(context.Background(), client, "", "", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "checkout" || len(got.Spec.Units) != 1 || got.Spec.Units[0].Name != "api" {
		t.Fatalf("unexpected source: %#v", got)
	}
}

func TestReadPackageSourceRejectsFleetSourceRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fleet := &kaprov1alpha1.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec:       kaprov1alpha1.FleetSpec{SourceRef: "checkout-source"},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fleet).Build()

	_, err := readPackageSource(context.Background(), client, "", "", "checkout")
	if err == nil || !strings.Contains(err.Error(), "pass --source checkout-source") {
		t.Fatalf("err=%v, want sourceRef guidance", err)
	}
}

func TestReadPackageSourceFromDeliveryUnit(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			Source: kaprov1alpha1.SourceSpec{
				Units: []kaprov1alpha1.Unit{{Name: "api", Version: "1.0.0"}},
			},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unit).Build()

	got, err := readPackageSource(context.Background(), client, "checkout", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "checkout" || len(got.Spec.Units) != 1 || got.Spec.Units[0].Name != "api" {
		t.Fatalf("unexpected source: %#v", got)
	}
}

func TestReadPackageSourceFallsBackToSameNamedDeliveryUnitForTargetSetFleet(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fleet := &kaprov1alpha1.Fleet{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			Source: kaprov1alpha1.SourceSpec{
				Units: []kaprov1alpha1.Unit{{Name: "api", Version: "1.0.0"}},
			},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fleet, unit).Build()

	got, err := readPackageSource(context.Background(), client, "", "", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "checkout" || len(got.Spec.Units) != 1 || got.Spec.Units[0].Name != "api" {
		t.Fatalf("unexpected source: %#v", got)
	}
}
