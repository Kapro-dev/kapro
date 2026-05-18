package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"k8s.io/client-go/tools/clientcmd"
)

type stubTokenSource struct {
	token string
	err   error
}

func (s *stubTokenSource) Token() (*oauth2.Token, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &oauth2.Token{AccessToken: s.token, Expiry: time.Now().Add(time.Hour)}, nil
}

func TestConnectGatewayURL(t *testing.T) {
	got := ConnectGatewayURL("my-project", "europe-west3", "fi-live")
	want := "https://connectgateway.googleapis.com/v1/projects/my-project/locations/europe-west3/gkeMemberships/fi-live"
	if got != want {
		t.Fatalf("ConnectGatewayURL = %q, want %q", got, want)
	}
}

func TestBuildConnectGatewayKubeconfig(t *testing.T) {
	raw, err := BuildConnectGatewayKubeconfig(
		context.Background(),
		"p1",
		"europe-west3",
		"cluster-a",
		&stubTokenSource{token: "ya29.fake-token"},
	)
	if err != nil {
		t.Fatalf("BuildConnectGatewayKubeconfig: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, "connectgateway.googleapis.com/v1/projects/p1/locations/europe-west3/gkeMemberships/cluster-a") {
		t.Errorf("kubeconfig missing Connect Gateway URL:\n%s", s)
	}
	if !strings.Contains(s, "ya29.fake-token") {
		t.Errorf("kubeconfig missing bearer token:\n%s", s)
	}
	if strings.Contains(s, "certificate-authority-data") {
		t.Errorf("kubeconfig should not embed a clusterCA (Connect Gateway uses public PKI):\n%s", s)
	}

	cfg, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatalf("clientcmd.Load: %v", err)
	}
	if cfg.CurrentContext == "" {
		t.Fatalf("kubeconfig has no current-context")
	}
	cluster := cfg.Clusters[cfg.Contexts[cfg.CurrentContext].Cluster]
	if cluster == nil || !strings.HasPrefix(cluster.Server, "https://connectgateway.googleapis.com/") {
		t.Errorf("cluster.Server not pointed at Connect Gateway: %+v", cluster)
	}
}

func TestBuildConnectGatewayKubeconfig_Validation(t *testing.T) {
	cases := []struct {
		name                          string
		project, location, membership string
	}{
		{"no project", "", "x", "m"},
		{"no location", "x", "", "m"},
		{"no membership", "x", "y", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildConnectGatewayKubeconfig(context.Background(), tc.project, tc.location, tc.membership, &stubTokenSource{token: "t"})
			if err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}
