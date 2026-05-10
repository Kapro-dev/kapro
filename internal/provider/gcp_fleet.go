package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GCPFleetProvider uses Fleet API for auto-discovery + direct GKE endpoint for access.
// Auth via GCP access token (refreshed by the controller on each reconcile).
// This is the recommended mode for GKE at scale — zero manual cluster registration.
type GCPFleetProvider struct {
	Project string
}

var _ Provider = (*GCPFleetProvider)(nil)

func (p *GCPFleetProvider) Name() string { return "gcp-fleet" }

func (p *GCPFleetProvider) GenerateKubeConfig(_ context.Context, clusterName string) ([]byte, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required for gcp-fleet provider")
	}

	// Resolve the cluster's GKE location from Fleet membership.
	membership, err := p.getMembership(clusterName)
	if err != nil {
		return nil, err
	}

	// Delegate to GCPBasicProvider for the actual kubeconfig generation.
	basic := &GCPBasicProvider{
		Project:  membership.project,
		Location: membership.location,
	}
	return basic.GenerateKubeConfig(context.Background(), membership.gkeClusterName)
}

// ListClusters discovers all Fleet memberships in the project.
func (p *GCPFleetProvider) ListClusters(_ context.Context) ([]ClusterInfo, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required")
	}

	out, err := execOutput("gcloud", "container", "fleet", "memberships", "list",
		"--project", p.Project,
		"--format=json(name,labels,endpoint.gkeCluster.resourceLink)",
	)
	if err != nil {
		return nil, fmt.Errorf("list Fleet memberships: %w", err)
	}

	var memberships []struct {
		Name     string            `json:"name"`
		Labels   map[string]string `json:"labels"`
		Endpoint struct {
			GKECluster struct {
				ResourceLink string `json:"resourceLink"`
			} `json:"gkeCluster"`
		} `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(out), &memberships); err != nil {
		return nil, fmt.Errorf("parse Fleet memberships: %w", err)
	}

	clusters := make([]ClusterInfo, 0, len(memberships))
	for _, m := range memberships {
		info := parseMembership(m.Name, m.Endpoint.GKECluster.ResourceLink)
		info.Labels = m.Labels
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
func (p *GCPFleetProvider) getMembership(membershipName string) (*membershipInfo, error) {
	out, err := execOutput("gcloud", "container", "fleet", "memberships", "describe", membershipName,
		"--project", p.Project,
		"--format=json(endpoint.gkeCluster.resourceLink)",
	)
	if err != nil {
		return nil, fmt.Errorf("describe Fleet membership %s: %w", membershipName, err)
	}

	var result struct {
		Endpoint struct {
			GKECluster struct {
				ResourceLink string `json:"resourceLink"`
			} `json:"gkeCluster"`
		} `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return nil, fmt.Errorf("parse membership %s: %w", membershipName, err)
	}

	rl := result.Endpoint.GKECluster.ResourceLink
	if rl == "" {
		return nil, fmt.Errorf("membership %s has no GKE cluster link", membershipName)
	}

	// Parse: //container.googleapis.com/projects/P/locations/L/clusters/C
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

// parseMembership extracts ClusterInfo from membership name and resource link.
func parseMembership(fullName, resourceLink string) ClusterInfo {
	name := fullName
	if parts := strings.Split(fullName, "/"); len(parts) > 0 {
		name = parts[len(parts)-1]
	}

	info := ClusterInfo{Name: name}
	if resourceLink == "" {
		return info
	}

	parts := strings.Split(resourceLink, "/")
	for i, p := range parts {
		switch p {
		case "projects":
			if i+1 < len(parts) {
				info.Project = parts[i+1]
			}
		case "locations":
			if i+1 < len(parts) {
				info.Location = parts[i+1]
			}
		}
	}
	return info
}
