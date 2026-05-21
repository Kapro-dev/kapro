package provider

import (
	"errors"
	"strings"
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestNewDiscoverer_GCP(t *testing.T) {
	d, err := NewDiscoverer(kaprov1alpha2.FleetClusterTemplateSource{
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

func TestNewDiscoverer_StubBranches(t *testing.T) {
	cases := []struct {
		name string
		src  kaprov1alpha2.FleetClusterTemplateSource
		want string
	}{
		{"aws", kaprov1alpha2.FleetClusterTemplateSource{AWS: &kaprov1alpha2.AWSFleetSource{Region: "eu-west-1"}}, "aws"},
		{"azure", kaprov1alpha2.FleetClusterTemplateSource{Azure: &kaprov1alpha2.AzureFleetSource{SubscriptionID: "sub"}}, "azure"},
		{"rhacm", kaprov1alpha2.FleetClusterTemplateSource{RHACM: &kaprov1alpha2.RHACMFleetSource{}}, "rhacm"},
		{"capi", kaprov1alpha2.FleetClusterTemplateSource{CAPI: &kaprov1alpha2.CAPIFleetSource{}}, "capi"},
		{"static", kaprov1alpha2.FleetClusterTemplateSource{Static: &kaprov1alpha2.StaticFleetSource{Clusters: []kaprov1alpha2.StaticClusterEntry{{Name: "x"}}}}, "static"},
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
	_, err := NewDiscoverer(kaprov1alpha2.FleetClusterTemplateSource{})
	if err == nil {
		t.Fatal("expected error when no source branch set")
	}
	if IsSourceNotImplemented(err) {
		t.Errorf("no-branch should not be reported as not-implemented")
	}
}

func TestNewDiscoverer_MultipleBranchesRejected(t *testing.T) {
	_, err := NewDiscoverer(kaprov1alpha2.FleetClusterTemplateSource{
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
