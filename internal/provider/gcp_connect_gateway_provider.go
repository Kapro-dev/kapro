package provider

import (
	"context"
	"fmt"
)

// GCPConnectGatewayProvider reaches a single GKE Fleet membership via Connect
// Gateway without performing Fleet discovery. Use this provider when you
// already know the project, location, and membership name (e.g. a pre-existing
// FleetCluster CR records them in spec.provider.parameters) and you don't need
// the gcp-fleet provider's ListClusters discovery loop.
//
// Why this exists as a first-class kind: private GKE clusters across projects
// or VPCs are routinely reachable via Connect Gateway with zero peering. The
// gcp-fleet provider already supports this internally, but selecting it implies
// the hub identity has gkehub.viewer on the Fleet for discovery — which is a
// strictly higher permission than the gatewayReader needed for a known
// membership. gcp-connect-gateway lets platform teams stay on least-privilege
// for the common "I know exactly which membership I want to talk to" case.
//
// Spec.Provider.Parameters keys consumed:
//
//	project     — GCP project hosting the Fleet membership (required)
//	location    — Fleet membership location, e.g. europe-west1 or "global" (required)
//	membership  — Fleet membership name (defaults to FleetCluster.metadata.name)
type GCPConnectGatewayProvider struct {
	Project    string
	Location   string
	Membership string // optional: defaults to clusterName arg
}

var _ Provider = (*GCPConnectGatewayProvider)(nil)

func (p *GCPConnectGatewayProvider) Name() string { return "gcp-connect-gateway" }

func (p *GCPConnectGatewayProvider) GenerateKubeConfig(ctx context.Context, clusterName string) ([]byte, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required for gcp-connect-gateway provider")
	}
	if p.Location == "" {
		return nil, fmt.Errorf("GCP location is required for gcp-connect-gateway provider")
	}
	membership := p.Membership
	if membership == "" {
		membership = clusterName
	}
	if membership == "" {
		return nil, fmt.Errorf("membership name is required (set provider.parameters.membership or pass clusterName)")
	}
	return BuildConnectGatewayKubeconfig(ctx, p.Project, p.Location, membership, nil)
}

// ListClusters returns an error: gcp-connect-gateway is a per-membership
// addressing provider, not a discovery provider. Use gcp-fleet for
// discovery.
func (p *GCPConnectGatewayProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	return nil, fmt.Errorf("gcp-connect-gateway provider does not support discovery; use gcp-fleet to list memberships")
}
