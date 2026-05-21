package main

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestReadPackageSourceFromFleetInlineSource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	source := &kaprov1alpha2.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha2.FleetSpec{
			Source: &kaprov1alpha2.SourceSpec{
				Units: []kaprov1alpha2.Unit{{Name: "api", Version: "1.0.0"}},
			},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()

	got, err := readPackageSource(context.Background(), client, "", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "checkout" || len(got.Spec.Units) != 1 || got.Spec.Units[0].Name != "api" {
		t.Fatalf("unexpected source: %#v", got)
	}
}

func TestReadPackageSourceRejectsFleetSourceRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fleet := &kaprov1alpha2.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec:       kaprov1alpha2.FleetSpec{SourceRef: "checkout-source"},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fleet).Build()

	_, err := readPackageSource(context.Background(), client, "", "checkout")
	if err == nil || !strings.Contains(err.Error(), "pass --source checkout-source") {
		t.Fatalf("err=%v, want sourceRef guidance", err)
	}
}
