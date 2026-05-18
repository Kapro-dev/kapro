package provider

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
)

// Connect Gateway helpers.
//
// Connect Gateway (https://cloud.google.com/anthos/multicluster-management/gateway)
// is a Google-hosted reverse proxy at connectgateway.googleapis.com. Each
// registered GKE Fleet membership runs a gke-connect-agent that maintains an
// outbound tunnel to Google; the hub authenticates to the gateway URL with an
// OAuth2 bearer token and Google relays the request through the agent into
// the cluster's private control plane.
//
// Why this matters: project, VPC, region, peering, authorized networks — none
// of it is relevant. As long as the hub identity has roles/gkehub.gatewayReader
// (or gatewayEditor for writes) on the membership and the spoke runs the
// connect agent, the hub can reach the cluster's apiserver.
//
// Connect Gateway is the implementation detail of the "gcp-fleet" provider;
// it is not a separately-selectable Provider kind. The helpers here are used
// by GCPFleetProvider.GenerateKubeConfig.

// ConnectGatewayURL builds the Connect Gateway endpoint for a given membership.
// Exposed for tests and for any controller that needs to surface the URL on a
// status field without constructing a full kubeconfig.
func ConnectGatewayURL(project, location, membership string) string {
	return fmt.Sprintf(
		"https://connectgateway.googleapis.com/v1/projects/%s/locations/%s/gkeMemberships/%s",
		project, location, membership,
	)
}

// BuildConnectGatewayKubeconfig returns a kubeconfig YAML that targets the
// Connect Gateway endpoint for the given GKE Fleet membership. The token
// source provides the OAuth2 bearer token (typically GCPTokenSource(ctx)).
//
// No clusterCA is embedded — Connect Gateway serves a public Google cert.
func BuildConnectGatewayKubeconfig(ctx context.Context, project, location, membership string, ts oauth2.TokenSource) ([]byte, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if location == "" {
		return nil, fmt.Errorf("location is required")
	}
	if membership == "" {
		return nil, fmt.Errorf("membership name is required")
	}
	if ts == nil {
		ts = GCPTokenSource(ctx)
	}
	tok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("get GCP access token: %w", err)
	}
	server := ConnectGatewayURL(project, location, membership)
	return []byte(gcpKubeconfig(membership, server, "", tok.AccessToken)), nil
}
