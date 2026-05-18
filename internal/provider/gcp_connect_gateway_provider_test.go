package provider

import (
	"context"
	"strings"
	"testing"
)

func TestNewGCPConnectGatewayProviderReturnsConcreteType(t *testing.T) {
	p, err := New("gcp-connect-gateway", Options{
		Project:    "my-project",
		Location:   "europe-west1",
		Membership: "de-prod-01",
	})
	if err != nil {
		t.Fatalf("New(gcp-connect-gateway): %v", err)
	}
	if p.Name() != "gcp-connect-gateway" {
		t.Fatalf("Name() = %q, want gcp-connect-gateway", p.Name())
	}
	if _, ok := p.(*GCPConnectGatewayProvider); !ok {
		t.Fatalf("expected *GCPConnectGatewayProvider, got %T", p)
	}
}

func TestGCPConnectGatewayProviderRequiresProjectAndLocation(t *testing.T) {
	tests := []struct {
		name     string
		p        *GCPConnectGatewayProvider
		wantErr  string
		callName string
	}{
		{
			name:    "missing project",
			p:       &GCPConnectGatewayProvider{Location: "global"},
			wantErr: "project is required",
		},
		{
			name:    "missing location",
			p:       &GCPConnectGatewayProvider{Project: "p"},
			wantErr: "location is required",
		},
		{
			name:    "missing membership and clusterName",
			p:       &GCPConnectGatewayProvider{Project: "p", Location: "global"},
			wantErr: "membership name is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.p.GenerateKubeConfig(context.Background(), tc.callName)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestGCPConnectGatewayProviderListClustersIsUnsupported(t *testing.T) {
	p := &GCPConnectGatewayProvider{Project: "p", Location: "global", Membership: "m"}
	_, err := p.ListClusters(context.Background())
	if err == nil {
		t.Fatal("expected error from ListClusters, got nil")
	}
	if !strings.Contains(err.Error(), "does not support discovery") {
		t.Fatalf("err = %v, want discovery-not-supported message", err)
	}
}

func TestConnectGatewayURLShape(t *testing.T) {
	got := ConnectGatewayURL("my-project", "europe-west1", "de-prod-01")
	want := "https://connectgateway.googleapis.com/v1/projects/my-project/locations/europe-west1/gkeMemberships/de-prod-01"
	if got != want {
		t.Fatalf("ConnectGatewayURL = %q, want %q", got, want)
	}
}
