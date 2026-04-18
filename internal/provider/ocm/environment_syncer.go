package ocm

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// EnvironmentSyncer watches OCM ManagedCluster objects and creates/updates
// Kapro Environment CRDs for each available spoke cluster.
type EnvironmentSyncer struct {
	provider  *Provider
	kapClient client.Client
}

// NewEnvironmentSyncer constructs an EnvironmentSyncer.
func NewEnvironmentSyncer(provider *Provider, kapClient client.Client) *EnvironmentSyncer {
	return &EnvironmentSyncer{
		provider:  provider,
		kapClient: kapClient,
	}
}

// Sync discovers OCM managed clusters and upserts Kapro Environment CRDs.
// Creates new Environments; updates labels on existing ones.
// Never deletes Environments — cluster removal is a manual operation.
func (s *EnvironmentSyncer) Sync(ctx context.Context) error {
	logger := log.FromContext(ctx)

	discovered, err := s.provider.SyncEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("ocm.EnvironmentSyncer.Sync: discover environments: %w", err)
	}

	for _, env := range discovered {
		var existing kaprov1alpha1.Environment
		err := s.kapClient.Get(ctx, client.ObjectKey{Name: env.Name, Namespace: env.Namespace}, &existing)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("ocm.EnvironmentSyncer.Sync: get environment %s: %w", env.Name, err)
			}
			if createErr := s.kapClient.Create(ctx, env); createErr != nil {
				logger.Error(createErr, "failed to create Environment", "name", env.Name)
				continue
			}
			logger.Info("created Environment from OCM ManagedCluster", "name", env.Name)
		} else {
			patch := client.MergeFrom(existing.DeepCopy())
			for k, v := range env.Labels {
				existing.Labels[k] = v
			}
			if patchErr := s.kapClient.Patch(ctx, &existing, patch); patchErr != nil {
				logger.Error(patchErr, "failed to patch Environment labels", "name", env.Name)
				continue
			}
			logger.V(1).Info("updated Environment labels from OCM ManagedCluster", "name", env.Name)
		}
	}
	return nil
}
