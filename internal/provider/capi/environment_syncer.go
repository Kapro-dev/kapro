package capi

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// EnvironmentSyncer watches CAPI Cluster objects on the management cluster and
// creates/updates corresponding Kapro Environment CRDs.
//
// Call Sync() periodically or on a watch event from CAPI Cluster resources.
type EnvironmentSyncer struct {
	provider   *Provider
	kapClient  client.Client
	namespace  string
}

// NewEnvironmentSyncer constructs an EnvironmentSyncer.
func NewEnvironmentSyncer(provider *Provider, kapClient client.Client, namespace string) *EnvironmentSyncer {
	return &EnvironmentSyncer{
		provider:  provider,
		kapClient: kapClient,
		namespace: namespace,
	}
}

// Sync discovers CAPI clusters and upserts Kapro Environment CRDs.
// It creates new Environments and updates existing ones (labels only).
// It never deletes Environments — cluster removal is a manual operation.
func (s *EnvironmentSyncer) Sync(ctx context.Context) error {
	logger := log.FromContext(ctx).WithValues("namespace", s.namespace)

	discovered, err := s.provider.SyncEnvironments(ctx, s.namespace)
	if err != nil {
		return fmt.Errorf("EnvironmentSyncer.Sync: discover environments: %w", err)
	}

	for _, env := range discovered {
		var existing kaprov1alpha1.Environment
		err := s.kapClient.Get(ctx, client.ObjectKey{Name: env.Name, Namespace: env.Namespace}, &existing)
		if err != nil {
			// Not found — create it.
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("EnvironmentSyncer.Sync: get environment %s: %w", env.Name, err)
			}
			if createErr := s.kapClient.Create(ctx, env); createErr != nil {
				logger.Error(createErr, "failed to create Environment", "name", env.Name)
				continue
			}
			logger.Info("created Environment from CAPI cluster", "name", env.Name)
		} else {
			// Already exists — update labels only.
			patch := client.MergeFrom(existing.DeepCopy())
			for k, v := range env.Labels {
				existing.Labels[k] = v
			}
			if patchErr := s.kapClient.Patch(ctx, &existing, patch); patchErr != nil {
				logger.Error(patchErr, "failed to patch Environment labels", "name", env.Name)
				continue
			}
			logger.V(1).Info("updated Environment labels from CAPI cluster", "name", env.Name)
		}
	}
	return nil
}
