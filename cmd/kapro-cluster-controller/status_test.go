package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func newStatusScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("core AddToScheme: %v", err)
	}
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("kapro AddToScheme: %v", err)
	}
	return s
}

func makeNode(name, kubeletVersion, region, zone, providerID string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				corev1.LabelTopologyRegion: region,
				corev1.LabelTopologyZone:   zone,
			},
		},
		Spec: corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: kubeletVersion},
		},
	}
}

func TestObserveCapabilities_HappyPath(t *testing.T) {
	scheme := newStatusScheme(t)
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeNode("node-1", "v1.30.0", "europe-west1", "europe-west1-b", "gce://proj/europe-west1-b/node-1"),
		makeNode("node-2", "v1.30.0", "europe-west1", "europe-west1-b", "gce://proj/europe-west1-b/node-2"),
		makeNode("node-3", "v1.30.0", "europe-west1", "europe-west1-c", "gce://proj/europe-west1-c/node-3"),
	).Build()

	sr := &statusReporter{Local: local, ClusterName: "de-prod-01"}
	caps, err := sr.observeCapabilities(context.Background())
	if err != nil {
		t.Fatalf("observeCapabilities: %v", err)
	}
	if caps.NodeCount != 3 {
		t.Errorf("NodeCount = %d, want 3", caps.NodeCount)
	}
	if caps.K8sVersion != "v1.30.0" {
		t.Errorf("K8sVersion = %q, want v1.30.0", caps.K8sVersion)
	}
	if caps.Region != "europe-west1" {
		t.Errorf("Region = %q, want europe-west1", caps.Region)
	}
	if caps.Zone != "europe-west1-b" {
		t.Errorf("Zone = %q, want europe-west1-b (majority)", caps.Zone)
	}
	if caps.Cloud != "gcp" {
		t.Errorf("Cloud = %q, want gcp", caps.Cloud)
	}
}

func TestObserveCapabilities_EmptyCluster(t *testing.T) {
	scheme := newStatusScheme(t)
	local := fake.NewClientBuilder().WithScheme(scheme).Build()

	sr := &statusReporter{Local: local, ClusterName: "empty"}
	caps, err := sr.observeCapabilities(context.Background())
	if err != nil {
		t.Fatalf("observeCapabilities: %v", err)
	}
	if caps.NodeCount != 0 {
		t.Errorf("expected zero nodes; got %d", caps.NodeCount)
	}
}

func TestObserveCapabilities_MixedVersions(t *testing.T) {
	scheme := newStatusScheme(t)
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeNode("n1", "v1.30.0", "us-east-1", "us-east-1a", "aws:///i-1"),
		makeNode("n2", "v1.30.0", "us-east-1", "us-east-1a", "aws:///i-2"),
		makeNode("n3", "v1.31.1", "us-east-1", "us-east-1b", "aws:///i-3"),
	).Build()
	sr := &statusReporter{Local: local, ClusterName: "x"}
	caps, _ := sr.observeCapabilities(context.Background())
	// v1.30.0 has 2 votes vs v1.31.1's 1.
	if caps.K8sVersion != "v1.30.0" {
		t.Errorf("majority K8sVersion = %q, want v1.30.0", caps.K8sVersion)
	}
}

func TestGuessCloud(t *testing.T) {
	cases := []struct {
		providerID, want string
	}{
		{"gce://proj/zone/node", "gcp"},
		{"aws:///i-123", "aws"},
		{"azure:///subscription/...", "azure"},
		{"digitalocean://12345", "digitalocean"},
		{"", ""},
		{"unknown://", ""},
	}
	for _, c := range cases {
		nodes := []corev1.Node{{Spec: corev1.NodeSpec{ProviderID: c.providerID}}}
		got := guessCloud(nodes)
		if got != c.want {
			t.Errorf("guessCloud(%q) = %q, want %q", c.providerID, got, c.want)
		}
	}
}

func TestMostFrequent_DeterministicTieBreak(t *testing.T) {
	// Equal counts: alphabetically smallest wins.
	got := mostFrequent(map[string]int{"b": 2, "a": 2, "c": 1})
	if got != "a" {
		t.Errorf("mostFrequent tie-break: got %q, want a", got)
	}
}

func TestStatusTick_PatchesCapabilities(t *testing.T) {
	scheme := newStatusScheme(t)
	fc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec:       kaprov1alpha1.FleetClusterSpec{},
	}
	hub := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fc).
		WithStatusSubresource(&kaprov1alpha1.FleetCluster{}).Build()
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeNode("n1", "v1.30.0", "europe-west1", "europe-west1-b", "gce://x"),
	).Build()

	sr := &statusReporter{
		Hub:               newHubClientFromStatic(hub),
		Local:             local,
		ClusterName:       "de-prod-01",
		ControllerVersion: "v0.0.0-test",
	}
	if err := sr.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got := &kaprov1alpha1.FleetCluster{}
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "de-prod-01"}, got); err != nil {
		t.Fatalf("get FleetCluster: %v", err)
	}
	if got.Status.Capabilities.NodeCount != 1 {
		t.Errorf("status not patched: NodeCount=%d", got.Status.Capabilities.NodeCount)
	}
	if got.Status.Capabilities.Cloud != "gcp" {
		t.Errorf("Cloud not patched: %q", got.Status.Capabilities.Cloud)
	}
	if got.Status.ControllerVersion != "v0.0.0-test" {
		t.Errorf("ControllerVersion = %q, want v0.0.0-test", got.Status.ControllerVersion)
	}
}

func TestStatusTick_HandlesMissingFleetCluster(t *testing.T) {
	scheme := newStatusScheme(t)
	hub := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.FleetCluster{}).Build()
	local := fake.NewClientBuilder().WithScheme(scheme).Build()
	sr := &statusReporter{Hub: newHubClientFromStatic(hub), Local: local, ClusterName: "nope"}
	if err := sr.tick(context.Background()); err == nil {
		t.Fatal("expected error when FleetCluster missing")
	}
}
