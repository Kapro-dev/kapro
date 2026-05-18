package provider

import (
	"context"
	"fmt"
	"strings"

	gkehub "cloud.google.com/go/gkehub/apiv1beta1"
	"cloud.google.com/go/gkehub/apiv1beta1/gkehubpb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GCPFleetProvider uses the GKE Fleet API for membership discovery and the
// GCP Connect Gateway for access. No gcloud CLI dependency.
//
// Connect Gateway is topology-agnostic: project, VPC, region, peering, and
// authorized-network configuration are irrelevant. Any cluster registered to
// a Fleet that the hub identity can read is reachable.
type GCPFleetProvider struct {
	Project string
}

var _ Provider = (*GCPFleetProvider)(nil)

func (p *GCPFleetProvider) Name() string { return "gcp-fleet" }

func (p *GCPFleetProvider) GenerateKubeConfig(ctx context.Context, membershipName string) ([]byte, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required for gcp-fleet provider")
	}

	// Look up the membership location so we can construct the Connect
	// Gateway URL. The membership name (not the underlying GKE cluster name)
	// is what Connect Gateway addresses by.
	loc, err := p.getMembershipLocation(ctx, membershipName)
	if err != nil {
		return nil, err
	}

	return BuildConnectGatewayKubeconfig(ctx, p.Project, loc, membershipName, nil)
}

// ListClusters discovers all Fleet memberships in the project.
func (p *GCPFleetProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required")
	}

	c, err := gkehub.NewGkeHubMembershipClient(ctx, option.WithTokenSource(GCPTokenSource(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create Fleet client: %w", err)
	}
	defer func() { _ = c.Close() }()

	parent := fmt.Sprintf("projects/%s/locations/-", p.Project)
	it := c.ListMemberships(ctx, &gkehubpb.ListMembershipsRequest{Parent: parent})

	var clusters []ClusterInfo
	for {
		m, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list Fleet memberships: %w", err)
		}

		info := parseMembership(m)
		info.Provider = "gcp-fleet"
		clusters = append(clusters, info)
	}

	return clusters, nil
}

// getMembershipLocation resolves a Fleet membership name to its location
// (e.g. "europe-west3", "global"). The location is required to construct the
// Connect Gateway URL but is not embedded in the membership name itself.
// Lists all memberships and filters by name — avoids the location requirement
// in the GetMembership API (which doesn't support locations/-).
func (p *GCPFleetProvider) getMembershipLocation(ctx context.Context, membershipName string) (string, error) {
	clusters, err := p.ListClusters(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range clusters {
		if c.Name == membershipName {
			if c.Location == "" {
				return "", fmt.Errorf("membership %s has no location", membershipName)
			}
			return c.Location, nil
		}
	}
	return "", fmt.Errorf("fleet membership %q not found in project %s", membershipName, p.Project)
}

// parseMembership extracts ClusterInfo from a Fleet membership proto.
func parseMembership(m *gkehubpb.Membership) ClusterInfo {
	// Full name: projects/P/locations/L/memberships/NAME
	fullName := m.GetName()
	parts := strings.Split(fullName, "/")

	name := fullName
	membershipLocation := ""
	for i, p := range parts {
		switch p {
		case "memberships":
			if i+1 < len(parts) {
				name = parts[i+1]
			}
		case "locations":
			if i+1 < len(parts) {
				membershipLocation = parts[i+1]
			}
		}
	}

	info := ClusterInfo{
		Name:     name,
		Labels:   m.GetLabels(),
		Location: membershipLocation, // Fleet membership location (e.g. europe-west1)
	}

	if ep := m.GetEndpoint(); ep != nil {
		if gke := ep.GetGkeCluster(); gke != nil {
			rl := gke.GetResourceLink()
			rlParts := strings.Split(rl, "/")
			for i, p := range rlParts {
				if p == "projects" && i+1 < len(rlParts) {
					info.Project = rlParts[i+1]
				}
			}
		}
	}

	return info
}
