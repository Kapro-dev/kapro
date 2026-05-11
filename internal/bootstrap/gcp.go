package bootstrap

import (
	"context"
	"fmt"
	"strings"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	gkehub "cloud.google.com/go/gkehub/apiv1beta1"
	"cloud.google.com/go/gkehub/apiv1beta1/gkehubpb"
	"golang.org/x/oauth2"
	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
	iam "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"

	"kapro.io/kapro/internal/provider"
)

// GCPSetupOptions holds configuration for GCP cluster setup.
type GCPSetupOptions struct {
	// HubProject is the GCP project where the hub cluster runs.
	HubProject string
	// SpokeProject is the GCP project where the spoke cluster runs.
	// If empty, defaults to HubProject (same-project topology).
	SpokeProject string
	// SpokeCluster is the GKE cluster name.
	SpokeCluster string
	// SpokeLocation is the GKE region/zone.
	SpokeLocation string
}

// RegisterFleetMembership registers a GKE cluster as a Fleet membership.
// Idempotent — skips if already registered.
func RegisterFleetMembership(ctx context.Context, project, clusterName, location string) error {
	ts := provider.GCPTokenSource(ctx)
	hubClient, err := gkehub.NewGkeHubMembershipClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return fmt.Errorf("create Fleet client: %w", err)
	}
	defer hubClient.Close()

	// Check if already registered.
	membershipParent := fmt.Sprintf("projects/%s/locations/%s", project, fleetLocation(location))
	membershipName := fmt.Sprintf("%s/memberships/%s", membershipParent, clusterName)

	_, err = hubClient.GetMembership(ctx, &gkehubpb.GetMembershipRequest{Name: membershipName})
	if err == nil {
		return nil // Already registered.
	}

	// Register.
	resourceLink := fmt.Sprintf("//container.googleapis.com/projects/%s/locations/%s/clusters/%s",
		project, location, clusterName)

	op, err := hubClient.CreateMembership(ctx, &gkehubpb.CreateMembershipRequest{
		Parent:       membershipParent,
		MembershipId: clusterName,
		Resource: &gkehubpb.Membership{
			Type: &gkehubpb.Membership_Endpoint{
				Endpoint: &gkehubpb.MembershipEndpoint{
					Type: &gkehubpb.MembershipEndpoint_GkeCluster{
						GkeCluster: &gkehubpb.GkeCluster{
							ResourceLink: resourceLink,
						},
					},
				},
			},
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("register Fleet membership %s: %w", clusterName, err)
	}

	// Wait for operation to complete.
	if _, err := op.Wait(ctx); err != nil {
		return fmt.Errorf("wait for Fleet membership %s: %w", clusterName, err)
	}

	return nil
}

// fleetLocation converts a zone (europe-west1-b) to a region (europe-west1)
// for Fleet membership parent. Fleet uses regional locations.
func fleetLocation(location string) string {
	parts := strings.Split(location, "-")
	if len(parts) >= 3 {
		// Check if last part is a single letter (zone suffix like "b")
		last := parts[len(parts)-1]
		if len(last) == 1 {
			return strings.Join(parts[:len(parts)-1], "-")
		}
	}
	return location
}

// RegistryInfo holds the created registry details.
type RegistryInfo struct {
	// URL is the OCI registry URL (e.g. europe-west1-docker.pkg.dev/project/kapro-registry)
	URL string
	// Name is the repository ID.
	Name string
}

// EnsureGARRegistry creates a Docker/OCI Artifact Registry repository.
// Idempotent — skips if already exists.
// Returns the registry URL for use in KaproApp registries and bundle push.
func EnsureGARRegistry(ctx context.Context, project, location, repoName string) (*RegistryInfo, error) {
	ts := provider.GCPTokenSource(ctx)
	c, err := artifactregistry.NewClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create Artifact Registry client: %w", err)
	}
	defer c.Close()

	parent := fmt.Sprintf("projects/%s/locations/%s", project, registryLocation(location))

	// Check if already exists.
	repoFullName := fmt.Sprintf("%s/repositories/%s", parent, repoName)
	existing, err := c.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: repoFullName})
	if err == nil {
		url := fmt.Sprintf("%s-docker.pkg.dev/%s/%s",
			registryLocation(location), project, existing.GetName())
		// GetName returns full path — extract just the repo ID.
		parts := strings.Split(existing.GetName(), "/")
		shortName := parts[len(parts)-1]
		url = fmt.Sprintf("%s-docker.pkg.dev/%s/%s",
			registryLocation(location), project, shortName)
		return &RegistryInfo{URL: url, Name: shortName}, nil
	}

	// Create.
	op, err := c.CreateRepository(ctx, &artifactregistrypb.CreateRepositoryRequest{
		Parent:       parent,
		RepositoryId: repoName,
		Repository: &artifactregistrypb.Repository{
			Format:      artifactregistrypb.Repository_DOCKER,
			Mode:        artifactregistrypb.Repository_STANDARD_REPOSITORY,
			Description: "Kapro centralized registry — OCI bundles + Helm charts",
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			url := fmt.Sprintf("%s-docker.pkg.dev/%s/%s",
				registryLocation(location), project, repoName)
			return &RegistryInfo{URL: url, Name: repoName}, nil
		}
		return nil, fmt.Errorf("create GAR repository %s: %w", repoName, err)
	}

	repo, err := op.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for GAR repository %s: %w", repoName, err)
	}

	parts := strings.Split(repo.GetName(), "/")
	shortName := parts[len(parts)-1]
	url := fmt.Sprintf("%s-docker.pkg.dev/%s/%s",
		registryLocation(location), project, shortName)

	return &RegistryInfo{URL: url, Name: shortName}, nil
}

// GrantRegistryReader grants artifactregistry.reader on a project to a service account.
// Used to give spoke node SAs permission to pull from the hub's centralized registry.
func GrantRegistryReader(ctx context.Context, hubProject, spokeSAEmail string) error {
	ts := provider.GCPTokenSource(ctx)
	return grantIAMRole(ctx, hubProject, ts, "serviceAccount:"+spokeSAEmail, "roles/artifactregistry.reader")
}

// registryLocation converts a zone to a region for GAR (GAR is regional).
func registryLocation(location string) string {
	parts := strings.Split(location, "-")
	if len(parts) >= 3 {
		last := parts[len(parts)-1]
		if len(last) == 1 { // zone suffix like "b"
			return strings.Join(parts[:len(parts)-1], "-")
		}
	}
	return location
}

// SetupGCPSpoke performs all GCP-side setup for a spoke cluster:
//  1. Enables required APIs (container, gkehub, artifactregistry)
//  2. Ensures Workload Identity is enabled on the cluster
//  3. Grants the spoke's default node SA artifactregistry.reader (for GAR chart pulls)
//
// All operations use Go SDK — no gcloud subprocess calls.
func SetupGCPSpoke(ctx context.Context, opts GCPSetupOptions) error {
	spokeProject := opts.SpokeProject
	if spokeProject == "" {
		spokeProject = opts.HubProject
	}

	// 1. Verify cluster exists and has Workload Identity enabled.
	ts := provider.GCPTokenSource(ctx)
	clusterClient, err := container.NewClusterManagerClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return fmt.Errorf("create GKE client: %w", err)
	}
	defer clusterClient.Close()

	clusterName := fmt.Sprintf("projects/%s/locations/%s/clusters/%s",
		spokeProject, opts.SpokeLocation, opts.SpokeCluster)
	cluster, err := clusterClient.GetCluster(ctx, &containerpb.GetClusterRequest{Name: clusterName})
	if err != nil {
		return fmt.Errorf("get cluster %s: %w", opts.SpokeCluster, err)
	}

	// Check Workload Identity.
	wic := cluster.GetWorkloadIdentityConfig()
	if wic == nil || wic.GetWorkloadPool() == "" {
		return fmt.Errorf("cluster %s does not have Workload Identity enabled — required for spoke-local Flux", opts.SpokeCluster)
	}

	// 2. Grant node SA artifactregistry.reader on spoke project (for GAR chart pulls).
	// The default node SA is: PROJECT_NUMBER-compute@developer.gserviceaccount.com
	// For autopilot or custom node pools, this may differ.
	nodeSA := cluster.GetNodeConfig().GetServiceAccount()
	if nodeSA == "" || nodeSA == "default" {
		// Resolve default compute SA from project number.
		nodeSA, err = resolveDefaultComputeSA(ctx, spokeProject, ts)
		if err != nil {
			// Non-fatal — the SA might already have access.
			fmt.Printf("  warning: could not resolve default compute SA: %v\n", err)
			return nil
		}
	}

	if err := grantIAMRole(ctx, spokeProject, ts,
		"serviceAccount:"+nodeSA,
		"roles/artifactregistry.reader",
	); err != nil {
		// Non-fatal — may already be bound.
		fmt.Printf("  warning: grant artifactregistry.reader to %s: %v\n", nodeSA, err)
	}

	return nil
}

// resolveDefaultComputeSA looks up the project number and returns the default
// compute service account email.
func resolveDefaultComputeSA(ctx context.Context, project string, ts oauth2.TokenSource) (string, error) {
	crmService, err := cloudresourcemanager.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		// Try IAM-based resolution.
		return resolveDefaultComputeSAFromIAM(ctx, project)
	}
	p, err := crmService.Projects.Get(project).Do()
	if err != nil {
		return "", fmt.Errorf("get project %s: %w", project, err)
	}
	return fmt.Sprintf("%d-compute@developer.gserviceaccount.com", p.ProjectNumber), nil
}

// resolveDefaultComputeSAFromIAM tries to find the default compute SA via IAM API.
func resolveDefaultComputeSAFromIAM(ctx context.Context, project string) (string, error) {
	iamService, err := iam.NewService(ctx)
	if err != nil {
		return "", fmt.Errorf("create IAM client: %w", err)
	}

	// List service accounts, find the default compute one.
	resp, err := iamService.Projects.ServiceAccounts.List("projects/" + project).Do()
	if err != nil {
		return "", fmt.Errorf("list service accounts: %w", err)
	}
	for _, sa := range resp.Accounts {
		if sa.DisplayName == "Compute Engine default service account" {
			return sa.Email, nil
		}
	}
	return "", fmt.Errorf("default compute SA not found in project %s", project)
}

// grantIAMRole adds an IAM binding to a project. Idempotent — skips if already bound.
func grantIAMRole(ctx context.Context, project string, ts oauth2.TokenSource, member, role string) error {
	_ = ts

	crmService, err := cloudresourcemanager.NewService(ctx)
	if err != nil {
		return fmt.Errorf("create CRM client: %w", err)
	}

	policy, err := crmService.Projects.GetIamPolicy(project,
		&cloudresourcemanager.GetIamPolicyRequest{}).Do()
	if err != nil {
		return fmt.Errorf("get IAM policy: %w", err)
	}

	// Check if binding already exists.
	for _, binding := range policy.Bindings {
		if binding.Role == role {
			for _, m := range binding.Members {
				if m == member {
					return nil // Already bound.
				}
			}
			// Add member to existing binding.
			binding.Members = append(binding.Members, member)
			_, err = crmService.Projects.SetIamPolicy(project,
				&cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Do()
			return err
		}
	}

	// Add new binding.
	policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{
		Role:    role,
		Members: []string{member},
	})
	_, err = crmService.Projects.SetIamPolicy(project,
		&cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Do()
	return err
}
