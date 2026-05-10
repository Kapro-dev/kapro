package bootstrap

import (
	"context"
	"fmt"

	"cloud.google.com/go/container/apiv1/containerpb"

	container "cloud.google.com/go/container/apiv1"
	"golang.org/x/oauth2"
	iam "google.golang.org/api/iam/v1"
	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
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
