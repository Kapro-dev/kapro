// Package gcputil provides GCP resource discovery using Go SDK.
// Lists projects, clusters, and Fleet memberships — no gcloud dependency.
package gcputil

import (
	"context"
	"fmt"
	"sort"
	"strings"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	gkehub "cloud.google.com/go/gkehub/apiv1beta1"
	"cloud.google.com/go/gkehub/apiv1beta1/gkehubpb"
	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"kapro.io/kapro/internal/provider"
)

// ProjectInfo holds basic project metadata.
type ProjectInfo struct {
	ID     string
	Name   string
	Number int64
}

// ClusterInfo holds GKE cluster metadata.
type ClusterInfo struct {
	Name      string
	Location  string
	Status    string
	Version   string
	NodeCount int32
	Autopilot bool
}

// FleetMember holds Fleet membership metadata.
type FleetMember struct {
	Name     string
	Location string
	Project  string // GKE cluster project (may differ from Fleet project)
	Cluster  string // GKE cluster name
	Labels   map[string]string
}

// RegistryInfo holds GAR repository metadata.
type RegistryInfo struct {
	Name     string
	Location string
	Format   string
	URL      string
}

// ListRegistries returns all Artifact Registry repositories in a project.
func ListRegistries(ctx context.Context, project, location string) ([]RegistryInfo, error) {
	ts := provider.GCPTokenSource(ctx)
	c, err := artifactregistry.NewClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create Artifact Registry client: %w", err)
	}
	defer c.Close()

	parent := fmt.Sprintf("projects/%s/locations/%s", project, location)
	it := c.ListRepositories(ctx, &artifactregistrypb.ListRepositoriesRequest{Parent: parent})

	var repos []RegistryInfo
	for {
		repo, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list repositories: %w", err)
		}

		// Parse name: projects/P/locations/L/repositories/NAME
		parts := strings.Split(repo.GetName(), "/")
		shortName := parts[len(parts)-1]
		loc := ""
		for i, p := range parts {
			if p == "locations" && i+1 < len(parts) {
				loc = parts[i+1]
			}
		}

		url := fmt.Sprintf("%s-docker.pkg.dev/%s/%s", loc, project, shortName)

		repos = append(repos, RegistryInfo{
			Name:     shortName,
			Location: loc,
			Format:   repo.GetFormat().String(),
			URL:      url,
		})
	}
	return repos, nil
}

// ListProjects returns all GCP projects accessible to the current identity.
func ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	ts := provider.GCPTokenSource(ctx)
	svc, err := cloudresourcemanager.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create CRM client: %w", err)
	}

	var projects []ProjectInfo
	err = svc.Projects.List().Filter("lifecycleState:ACTIVE").Pages(ctx, func(resp *cloudresourcemanager.ListProjectsResponse) error {
		for _, p := range resp.Projects {
			projects = append(projects, ProjectInfo{
				ID:     p.ProjectId,
				Name:   p.Name,
				Number: p.ProjectNumber,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	return projects, nil
}

// ListClusters returns all GKE clusters in a project (all locations).
func ListClusters(ctx context.Context, project string) ([]ClusterInfo, error) {
	ts := provider.GCPTokenSource(ctx)
	c, err := container.NewClusterManagerClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create GKE client: %w", err)
	}
	defer c.Close()

	parent := fmt.Sprintf("projects/%s/locations/-", project)
	resp, err := c.ListClusters(ctx, &containerpb.ListClustersRequest{Parent: parent})
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}

	var clusters []ClusterInfo
	for _, cl := range resp.GetClusters() {
		nodeCount := int32(0)
		for _, np := range cl.GetNodePools() {
			nodeCount += np.GetInitialNodeCount()
		}
		clusters = append(clusters, ClusterInfo{
			Name:      cl.GetName(),
			Location:  cl.GetLocation(),
			Status:    cl.GetStatus().String(),
			Version:   cl.GetCurrentMasterVersion(),
			NodeCount: nodeCount,
			Autopilot: cl.GetAutopilot().GetEnabled(),
		})
	}
	return clusters, nil
}

// ListFleetMembers returns all Fleet memberships in a project.
func ListFleetMembers(ctx context.Context, project string) ([]FleetMember, error) {
	ts := provider.GCPTokenSource(ctx)
	c, err := gkehub.NewGkeHubMembershipClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create Fleet client: %w", err)
	}
	defer c.Close()

	parent := fmt.Sprintf("projects/%s/locations/-", project)
	it := c.ListMemberships(ctx, &gkehubpb.ListMembershipsRequest{Parent: parent})

	var members []FleetMember
	for {
		m, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list Fleet memberships: %w", err)
		}

		member := FleetMember{
			Labels: m.GetLabels(),
		}

		// Parse name: projects/P/locations/L/memberships/NAME
		parts := strings.Split(m.GetName(), "/")
		for i, p := range parts {
			switch p {
			case "memberships":
				if i+1 < len(parts) {
					member.Name = parts[i+1]
				}
			case "locations":
				if i+1 < len(parts) {
					member.Location = parts[i+1]
				}
			}
		}

		// Parse GKE cluster info from endpoint.
		if ep := m.GetEndpoint(); ep != nil {
			if gke := ep.GetGkeCluster(); gke != nil {
				rl := gke.GetResourceLink()
				rlParts := strings.Split(rl, "/")
				for i, p := range rlParts {
					switch p {
					case "projects":
						if i+1 < len(rlParts) {
							member.Project = rlParts[i+1]
						}
					case "clusters":
						if i+1 < len(rlParts) {
							member.Cluster = rlParts[i+1]
						}
					}
				}
			}
		}

		members = append(members, member)
	}
	return members, nil
}
