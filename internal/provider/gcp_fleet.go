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

// GCPFleetProvider uses Fleet API for auto-discovery + direct GKE endpoint for access.
// No gcloud CLI dependency — uses Go GKE Hub SDK for Fleet membership listing.
type GCPFleetProvider struct {
	Project string
}

var _ Provider = (*GCPFleetProvider)(nil)

func (p *GCPFleetProvider) Name() string { return "gcp-fleet" }

func (p *GCPFleetProvider) GenerateKubeConfig(ctx context.Context, clusterName string) ([]byte, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required for gcp-fleet provider")
	}

	// Resolve the cluster's GKE location from Fleet membership.
	membership, err := p.getMembership(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	// Delegate to GCPBasicProvider for kubeconfig generation.
	basic := &GCPBasicProvider{
		Project:  membership.project,
		Location: membership.location,
	}
	return basic.GenerateKubeConfig(ctx, membership.gkeClusterName)
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

type membershipInfo struct {
	project        string
	location       string
	gkeClusterName string
}

// getMembership resolves a Fleet membership to its GKE cluster details.
// Lists all memberships and filters by name — avoids the location requirement
// in the GetMembership API (which doesn't support locations/-).
func (p *GCPFleetProvider) getMembership(ctx context.Context, membershipName string) (*membershipInfo, error) {
	clusters, err := p.ListClusters(ctx)
	if err != nil {
		return nil, err
	}

	for _, c := range clusters {
		if c.Name == membershipName {
			if c.Project == "" || c.Location == "" {
				return nil, fmt.Errorf("membership %s has incomplete cluster info", membershipName)
			}
			// Resolve the actual GKE cluster name from the resource link.
			// For Fleet memberships, the cluster name may differ from the membership name.
			// We need to get the full membership to parse the resource link.
			return p.getMembershipByFullName(ctx, c)
		}
	}
	return nil, fmt.Errorf("Fleet membership %q not found in project %s", membershipName, p.Project)
}

func (p *GCPFleetProvider) getMembershipByFullName(ctx context.Context, ci ClusterInfo) (*membershipInfo, error) {
	c, err := gkehub.NewGkeHubMembershipClient(ctx, option.WithTokenSource(GCPTokenSource(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create Fleet client: %w", err)
	}
	defer func() { _ = c.Close() }()

	// Use the actual location from ListClusters, not wildcard.
	name := fmt.Sprintf("projects/%s/locations/%s/memberships/%s", p.Project, ci.Location, ci.Name)
	m, err := c.GetMembership(ctx, &gkehubpb.GetMembershipRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("get Fleet membership %s: %w", name, err)
	}

	ep := m.GetEndpoint()
	if ep == nil {
		return nil, fmt.Errorf("membership %s has no endpoint", ci.Name)
	}
	gkeCluster := ep.GetGkeCluster()
	if gkeCluster == nil {
		return nil, fmt.Errorf("membership %s is not a GKE cluster", ci.Name)
	}

	return parseResourceLink(gkeCluster.GetResourceLink())
}

func parseResourceLink(rl string) (*membershipInfo, error) {
	if rl == "" {
		return nil, fmt.Errorf("empty resource link")
	}

	parts := strings.Split(rl, "/")
	info := &membershipInfo{}
	for i, part := range parts {
		switch part {
		case "projects":
			if i+1 < len(parts) {
				info.project = parts[i+1]
			}
		case "locations":
			if i+1 < len(parts) {
				info.location = parts[i+1]
			}
		case "clusters":
			if i+1 < len(parts) {
				info.gkeClusterName = parts[i+1]
			}
		}
	}

	if info.project == "" || info.location == "" || info.gkeClusterName == "" {
		return nil, fmt.Errorf("could not parse resource link %q", rl)
	}
	return info, nil
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
