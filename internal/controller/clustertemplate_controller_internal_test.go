package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/provider"
)

// stubDiscoverer is an in-memory Discoverer used by the reconciler tests so
// we never hit a live cloud API.
type stubDiscoverer struct {
	clusters []provider.ClusterInfo
	err      error
	provider kaprov1alpha2.ClusterProvider
	source   string
}

func (s *stubDiscoverer) List(_ context.Context) ([]provider.ClusterInfo, error) {
	return s.clusters, s.err
}
func (s *stubDiscoverer) Provider() kaprov1alpha2.ClusterProvider { return s.provider }
func (s *stubDiscoverer) SourceKind() string                      { return s.source }

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newGCPStubFactory(memberships []provider.ClusterInfo) DiscovererFactory {
	return func(_ kaprov1alpha2.ClusterTemplateSource) (provider.Discoverer, error) {
		return &stubDiscoverer{
			clusters: memberships,
			provider: kaprov1alpha2.ClusterProvider{
				Kind:       "gcp-fleet",
				Parameters: map[string]string{"project": "p1"},
			},
			source: "gcp",
		}, nil
	}
}

func TestFleetClusterTemplate_StaticSourceImportsKubeconfigRefs(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	tmpl := &kaprov1alpha2.ClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "static-sites"},
		Spec: kaprov1alpha2.ClusterTemplateSpec{
			Source: kaprov1alpha2.ClusterTemplateSource{
				Static: &kaprov1alpha2.StaticFleetSource{Clusters: []kaprov1alpha2.StaticClusterEntry{
					{
						Name: "edge-berlin",
						KubeconfigSecretRef: &corev1.SecretReference{
							Namespace: "kapro-system",
							Name:      "edge-berlin-kubeconfig",
						},
						Labels: map[string]string{"site": "berlin", "env": "prod"},
					},
				}},
			},
			Template: kaprov1alpha2.ClusterTemplateBody{
				Metadata: kaprov1alpha2.ClusterTemplateMetadata{
					Labels: map[string]string{"owner": "platform"},
				},
				Spec: kaprov1alpha2.ClusterSpec{
					Delivery: kaprov1alpha2.DeliverySpec{Mode: kaprov1alpha2.DeliveryModePull, BackendRef: "oci"},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.ClusterTemplate{}).
		WithObjects(tmpl).Build()

	r := &ClusterTemplateReconciler{Client: c, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: tmpl.Name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got kaprov1alpha2.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: "edge-berlin"}, &got); err != nil {
		t.Fatalf("get imported cluster: %v", err)
	}
	if got.Spec.Provider == nil {
		t.Fatal("expected derived provider")
	}
	if got.Spec.Provider.Kind != "kubeconfig" {
		t.Fatalf("provider kind = %q, want kubeconfig", got.Spec.Provider.Kind)
	}
	if got.Spec.Provider.Parameters["secretName"] != "edge-berlin-kubeconfig" {
		t.Errorf("secretName = %q, want edge-berlin-kubeconfig", got.Spec.Provider.Parameters["secretName"])
	}
	if got.Spec.Provider.Parameters["secretNamespace"] != "kapro-system" {
		t.Errorf("secretNamespace = %q, want kapro-system", got.Spec.Provider.Parameters["secretNamespace"])
	}
	if got.Labels["site"] != "berlin" || got.Labels["owner"] != "platform" {
		t.Errorf("labels were not merged correctly: %v", got.Labels)
	}

	var refreshed kaprov1alpha2.ClusterTemplate
	if err := c.Get(ctx, client.ObjectKey{Name: tmpl.Name}, &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.Status.SourceKind != "static" {
		t.Errorf("status.SourceKind = %q, want static", refreshed.Status.SourceKind)
	}
	if refreshed.Status.ImportedClusters != 1 {
		t.Errorf("status.ImportedClusters = %d, want 1", refreshed.Status.ImportedClusters)
	}
}

func TestFleetClusterTemplate_ImportsDiscoveredClusters(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	tmpl := &kaprov1alpha2.ClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "lidl-gke"},
		Spec: kaprov1alpha2.ClusterTemplateSpec{
			Source: kaprov1alpha2.ClusterTemplateSource{
				GCP: &kaprov1alpha2.GCPFleetSource{Project: "p1"},
			},
			Interval: "5m",
			Template: kaprov1alpha2.ClusterTemplateBody{
				Metadata: kaprov1alpha2.ClusterTemplateMetadata{
					Labels: map[string]string{"managed-by": "kapro"},
				},
				Spec: kaprov1alpha2.ClusterSpec{
					Delivery: kaprov1alpha2.DeliverySpec{
						Mode:       kaprov1alpha2.DeliveryModePull,
						BackendRef: "oci",
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.ClusterTemplate{}).
		WithObjects(tmpl).
		Build()

	r := &ClusterTemplateReconciler{
		Client: c,
		Scheme: scheme,
		DiscovererFactory: newGCPStubFactory([]provider.ClusterInfo{
			{Name: "fi-live", Project: "p1", Location: "europe-west3", Labels: map[string]string{"env": "prod"}},
			{Name: "de-live", Project: "p1", Location: "europe-west3", Labels: map[string]string{"env": "prod"}},
		}),
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: tmpl.Name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var fcs kaprov1alpha2.ClusterList
	if err := c.List(ctx, &fcs); err != nil {
		t.Fatal(err)
	}
	if len(fcs.Items) != 2 {
		t.Fatalf("imported FleetCluster count = %d, want 2", len(fcs.Items))
	}
	for _, fc := range fcs.Items {
		if fc.Labels[kaprov1alpha2.ClusterTemplateManagedByLabel] != kaprov1alpha2.ClusterTemplateManagedByValue {
			t.Errorf("%s missing managed-by label: %v", fc.Name, fc.Labels)
		}
		if fc.Labels[kaprov1alpha2.ClusterTemplateNameLabel] != tmpl.Name {
			t.Errorf("%s missing template-name label", fc.Name)
		}
		if fc.Spec.Provider == nil || fc.Spec.Provider.Kind != "gcp-fleet" {
			t.Errorf("%s missing derived provider: %+v", fc.Name, fc.Spec.Provider)
		}
		if fc.Spec.Delivery.BackendRef != "oci" {
			t.Errorf("%s wrong backendRef: %q", fc.Name, fc.Spec.Delivery.BackendRef)
		}
		if len(fc.OwnerReferences) != 1 || fc.OwnerReferences[0].Name != tmpl.Name {
			t.Errorf("%s missing ownerReference to template", fc.Name)
		}
	}

	var got kaprov1alpha2.ClusterTemplate
	if err := c.Get(ctx, client.ObjectKey{Name: tmpl.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ImportedClusters != 2 {
		t.Errorf("status.ImportedClusters = %d, want 2", got.Status.ImportedClusters)
	}
	if got.Status.SourceKind != "gcp" {
		t.Errorf("status.SourceKind = %q, want gcp", got.Status.SourceKind)
	}
}

func TestFleetClusterTemplate_SelectorFilters(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	tmpl := &kaprov1alpha2.ClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-only"},
		Spec: kaprov1alpha2.ClusterTemplateSpec{
			Source: kaprov1alpha2.ClusterTemplateSource{
				GCP: &kaprov1alpha2.GCPFleetSource{Project: "p1"},
			},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
			Template: kaprov1alpha2.ClusterTemplateBody{
				Spec: kaprov1alpha2.ClusterSpec{
					Delivery: kaprov1alpha2.DeliverySpec{Mode: kaprov1alpha2.DeliveryModePull, BackendRef: "oci"},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.ClusterTemplate{}).
		WithObjects(tmpl).Build()

	r := &ClusterTemplateReconciler{
		Client: c, Scheme: scheme,
		DiscovererFactory: newGCPStubFactory([]provider.ClusterInfo{
			{Name: "prod-1", Labels: map[string]string{"env": "prod"}},
			{Name: "dev-1", Labels: map[string]string{"env": "dev"}},
		}),
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: tmpl.Name}}); err != nil {
		t.Fatal(err)
	}

	var fcs kaprov1alpha2.ClusterList
	_ = c.List(ctx, &fcs)
	if len(fcs.Items) != 1 || fcs.Items[0].Name != "prod-1" {
		t.Errorf("expected only prod-1; got %+v", fcs.Items)
	}
}

func TestFleetClusterTemplate_LeavesUnmanagedClustersAlone(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	preExisting := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "fi-live",
			Labels: map[string]string{"hand": "authored"},
		},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: kaprov1alpha2.DeliveryModePush, BackendRef: "flux"},
		},
	}
	tmpl := &kaprov1alpha2.ClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl"},
		Spec: kaprov1alpha2.ClusterTemplateSpec{
			Source:   kaprov1alpha2.ClusterTemplateSource{GCP: &kaprov1alpha2.GCPFleetSource{Project: "p1"}},
			Template: kaprov1alpha2.ClusterTemplateBody{Spec: kaprov1alpha2.ClusterSpec{Delivery: kaprov1alpha2.DeliverySpec{Mode: kaprov1alpha2.DeliveryModePull, BackendRef: "oci"}}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.ClusterTemplate{}).
		WithObjects(tmpl, preExisting).Build()

	r := &ClusterTemplateReconciler{
		Client: c, Scheme: scheme,
		DiscovererFactory: newGCPStubFactory([]provider.ClusterInfo{
			{Name: "fi-live", Labels: map[string]string{}},
		}),
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: tmpl.Name}}); err != nil {
		t.Fatal(err)
	}

	var got kaprov1alpha2.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: "fi-live"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels["hand"] != "authored" {
		t.Errorf("hand-authored label lost: %v", got.Labels)
	}
	if got.Labels[kaprov1alpha2.ClusterTemplateManagedByLabel] == kaprov1alpha2.ClusterTemplateManagedByValue {
		t.Errorf("unmanaged FleetCluster was claimed by the template")
	}
	if got.Spec.Delivery.BackendRef != "flux" {
		t.Errorf("hand-authored spec mutated: %+v", got.Spec.Delivery)
	}
}

func TestFleetClusterTemplate_Suspend(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	tmpl := &kaprov1alpha2.ClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "paused"},
		Spec: kaprov1alpha2.ClusterTemplateSpec{
			Source:   kaprov1alpha2.ClusterTemplateSource{GCP: &kaprov1alpha2.GCPFleetSource{Project: "p1"}},
			Suspend:  true,
			Template: kaprov1alpha2.ClusterTemplateBody{Spec: kaprov1alpha2.ClusterSpec{Delivery: kaprov1alpha2.DeliverySpec{Mode: kaprov1alpha2.DeliveryModePull, BackendRef: "oci"}}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.ClusterTemplate{}).
		WithObjects(tmpl).Build()

	called := false
	r := &ClusterTemplateReconciler{
		Client: c, Scheme: scheme,
		DiscovererFactory: func(_ kaprov1alpha2.ClusterTemplateSource) (provider.Discoverer, error) {
			called = true
			return nil, errors.New("should not be called when suspended")
		},
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: tmpl.Name}}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Errorf("Discoverer was invoked despite suspend=true")
	}

	var fcs kaprov1alpha2.ClusterList
	_ = c.List(ctx, &fcs)
	if len(fcs.Items) != 0 {
		t.Errorf("clusters imported despite suspend: %d", len(fcs.Items))
	}
}

func TestFleetClusterTemplate_SourceNotImplementedSurfacesCondition(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	tmpl := &kaprov1alpha2.ClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-stub"},
		Spec: kaprov1alpha2.ClusterTemplateSpec{
			Source:   kaprov1alpha2.ClusterTemplateSource{AWS: &kaprov1alpha2.AWSFleetSource{Region: "eu-west-1"}},
			Template: kaprov1alpha2.ClusterTemplateBody{Spec: kaprov1alpha2.ClusterSpec{Delivery: kaprov1alpha2.DeliverySpec{Mode: kaprov1alpha2.DeliveryModePush, BackendRef: "flux"}}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.ClusterTemplate{}).
		WithObjects(tmpl).Build()

	// Use the real factory so we exercise the not-implemented path.
	r := &ClusterTemplateReconciler{Client: c, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: tmpl.Name}}); err != nil {
		t.Fatal(err)
	}

	var got kaprov1alpha2.ClusterTemplate
	if err := c.Get(ctx, client.ObjectKey{Name: tmpl.Name}, &got); err != nil {
		t.Fatal(err)
	}
	var readyReason, stalledStatus string
	for _, cond := range got.Status.Conditions {
		switch cond.Type {
		case kaprov1alpha2.ConditionTypeReady:
			readyReason = cond.Reason
		case kaprov1alpha2.ConditionTypeStalled:
			stalledStatus = string(cond.Status)
		}
	}
	if readyReason != "SourceNotImplemented" {
		t.Errorf("Ready reason = %q, want SourceNotImplemented", readyReason)
	}
	// Stalled must mirror Ready=False for non-recoverable failures so existing
	// "kubectl get fct -o wide | grep Stalled" alerting picks this up.
	if stalledStatus != "True" {
		t.Errorf("Stalled status = %q, want True", stalledStatus)
	}
	// SourceKind must still populate so the printcolumn isn't blank for stubs.
	if got.Status.SourceKind != "aws" {
		t.Errorf("status.SourceKind = %q, want aws", got.Status.SourceKind)
	}
}

func TestFleetClusterTemplate_PrunesOrphans(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	tmpl := &kaprov1alpha2.ClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "prune-on"},
		Spec: kaprov1alpha2.ClusterTemplateSpec{
			Source:   kaprov1alpha2.ClusterTemplateSource{GCP: &kaprov1alpha2.GCPFleetSource{Project: "p1"}},
			Prune:    true,
			Template: kaprov1alpha2.ClusterTemplateBody{Spec: kaprov1alpha2.ClusterSpec{Delivery: kaprov1alpha2.DeliverySpec{Mode: kaprov1alpha2.DeliveryModePull, BackendRef: "oci"}}},
		},
	}
	orphan := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gone",
			Labels: map[string]string{
				kaprov1alpha2.ClusterTemplateManagedByLabel: kaprov1alpha2.ClusterTemplateManagedByValue,
				kaprov1alpha2.ClusterTemplateNameLabel:      "prune-on",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.ClusterTemplate{}).
		WithObjects(tmpl, orphan).Build()

	r := &ClusterTemplateReconciler{
		Client: c, Scheme: scheme,
		DiscovererFactory: newGCPStubFactory([]provider.ClusterInfo{
			{Name: "alive"},
		}),
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: tmpl.Name}}); err != nil {
		t.Fatal(err)
	}

	var deleted kaprov1alpha2.Cluster
	err := c.Get(ctx, client.ObjectKey{Name: "gone"}, &deleted)
	if err == nil {
		t.Errorf("orphan FleetCluster was not deleted")
	}
}
