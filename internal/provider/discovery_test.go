package provider

import (
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestNewDiscoverer_GCP(t *testing.T) {
	d, err := NewDiscoverer(kaprov1alpha2.ClusterTemplateSource{
		GCP: &kaprov1alpha2.GCPFleetSource{Project: "p1"},
	})
	if err != nil {
		t.Fatalf("NewDiscoverer: %v", err)
	}
	if d.SourceKind() != "gcp" {
		t.Errorf("SourceKind() = %q, want gcp", d.SourceKind())
	}
	p := d.Provider()
	if p.Kind != "gcp-fleet" {
		t.Errorf("Provider().Kind = %q, want gcp-fleet", p.Kind)
	}
	if p.Parameters["project"] != "p1" {
		t.Errorf("Provider().Parameters[project] = %q, want p1", p.Parameters["project"])
	}
}

func TestNewDiscoverer_Static(t *testing.T) {
	d, err := NewDiscoverer(kaprov1alpha2.ClusterTemplateSource{
		Static: &kaprov1alpha2.StaticFleetSource{Clusters: []kaprov1alpha2.StaticClusterEntry{
			{
				Name: "edge-1",
				KubeconfigSecretRef: &corev1.SecretReference{
					Namespace: "kapro-system",
					Name:      "edge-1-kubeconfig",
				},
				Labels: map[string]string{"site": "berlin", "env": "prod"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewDiscoverer: %v", err)
	}
	if d.SourceKind() != "static" {
		t.Errorf("SourceKind() = %q, want static", d.SourceKind())
	}
	p := d.Provider()
	if p.Kind != "kubeconfig" {
		t.Errorf("Provider().Kind = %q, want kubeconfig", p.Kind)
	}
	clusters, err := d.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("List returned %d clusters, want 1", len(clusters))
	}
	got := clusters[0]
	if got.Name != "edge-1" {
		t.Errorf("Name = %q, want edge-1", got.Name)
	}
	if got.Labels["site"] != "berlin" {
		t.Errorf("Labels[site] = %q, want berlin", got.Labels["site"])
	}
	if got.Provider != "kubeconfig" {
		t.Errorf("Provider = %q, want kubeconfig", got.Provider)
	}
	if got.ProviderParameters["secretName"] != "edge-1-kubeconfig" {
		t.Errorf("secretName = %q, want edge-1-kubeconfig", got.ProviderParameters["secretName"])
	}
	if got.ProviderParameters["secretNamespace"] != "kapro-system" {
		t.Errorf("secretNamespace = %q, want kapro-system", got.ProviderParameters["secretNamespace"])
	}
}

func TestNewDiscoverer_StubBranches(t *testing.T) {
	cases := []struct {
		name string
		src  kaprov1alpha2.ClusterTemplateSource
		want string
	}{
		{"aws", kaprov1alpha2.ClusterTemplateSource{AWS: &kaprov1alpha2.AWSFleetSource{Region: "eu-west-1"}}, "aws"},
		{"azure", kaprov1alpha2.ClusterTemplateSource{Azure: &kaprov1alpha2.AzureFleetSource{SubscriptionID: "sub"}}, "azure"},
		{"rhacm", kaprov1alpha2.ClusterTemplateSource{RHACM: &kaprov1alpha2.RHACMFleetSource{}}, "rhacm"},
		{"capi", kaprov1alpha2.ClusterTemplateSource{CAPI: &kaprov1alpha2.CAPIFleetSource{}}, "capi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDiscoverer(tc.src)
			if err == nil {
				t.Fatalf("expected ErrSourceNotImplemented for branch %s", tc.name)
			}
			var ne ErrSourceNotImplemented
			if !errors.As(err, &ne) {
				t.Fatalf("want ErrSourceNotImplemented, got %T: %v", err, err)
			}
			if ne.Branch != tc.want {
				t.Errorf("Branch = %q, want %q", ne.Branch, tc.want)
			}
			if !IsSourceNotImplemented(err) {
				t.Errorf("IsSourceNotImplemented returned false")
			}
		})
	}
}

func TestNewDiscoverer_NoBranch(t *testing.T) {
	_, err := NewDiscoverer(kaprov1alpha2.ClusterTemplateSource{})
	if err == nil {
		t.Fatal("expected error when no source branch set")
	}
	if IsSourceNotImplemented(err) {
		t.Errorf("no-branch should not be reported as not-implemented")
	}
}

func TestNewDiscoverer_MultipleBranchesRejected(t *testing.T) {
	_, err := NewDiscoverer(kaprov1alpha2.ClusterTemplateSource{
		GCP: &kaprov1alpha2.GCPFleetSource{Project: "p1"},
		AWS: &kaprov1alpha2.AWSFleetSource{Region: "eu-west-1"},
	})
	if err == nil {
		t.Fatal("expected error when multiple source branches set")
	}
	if IsSourceNotImplemented(err) {
		t.Errorf("multi-branch should not be reported as not-implemented")
	}
	if !strings.Contains(err.Error(), "gcp") || !strings.Contains(err.Error(), "aws") {
		t.Errorf("error message %q should name conflicting branches", err.Error())
	}
}
