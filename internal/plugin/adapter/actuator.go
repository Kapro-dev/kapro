package adapter

import (
	"context"
	"fmt"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"

	"google.golang.org/grpc"
)

// ActuatorAdapter adapts a KAI gRPC plugin to pkg/actuator.Actuator.
type ActuatorAdapter struct {
	name       string
	client     kaiv1alpha1.ActuatorServiceClient
	timeout    time.Duration
	parameters map[string]string
	conn       *grpc.ClientConn
}

// NewActuatorAdapter returns an actuator adapter backed by a KAI client.
func NewActuatorAdapter(reg kaprov1alpha1.PluginRegistration, client kaiv1alpha1.ActuatorServiceClient) (*ActuatorAdapter, error) {
	if reg.Spec.Type != kaprov1alpha1.PluginTypeActuator {
		return nil, fmt.Errorf("plugin %q is %q, expected %q", reg.Name, reg.Spec.Type, kaprov1alpha1.PluginTypeActuator)
	}
	if client == nil {
		return nil, fmt.Errorf("actuator plugin client is nil")
	}
	if err := validateRegistration(reg); err != nil {
		return nil, err
	}
	timeout, err := timeoutFor(reg)
	if err != nil {
		return nil, err
	}
	return &ActuatorAdapter{
		name:       reg.Spec.Name,
		client:     client,
		timeout:    timeout,
		parameters: copyParameters(reg.Spec.Parameters),
	}, nil
}

// Apply instructs the external plugin to apply a version to one target.
func (a *ActuatorAdapter) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	clusterName, err := clusterName(req.Cluster)
	if err != nil {
		return err
	}
	rpcCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	resp, err := a.client.Apply(rpcCtx, &kaiv1alpha1.ApplyRequest{
		Target:          clusterName,
		Version:         req.Version,
		PreviousVersion: req.PreviousVersion,
		Parameters:      a.parametersWithAppKey(req.AppKey),
	})
	if err != nil {
		return fmt.Errorf("apply via actuator plugin %q: %w", a.name, err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("actuator plugin %q rejected apply: %s", a.name, resp.GetMessage())
	}
	return nil
}

// IsConverged asks the external plugin whether a target has converged.
func (a *ActuatorAdapter) IsConverged(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, version, appKey string) (bool, error) {
	clusterName, err := clusterName(cluster)
	if err != nil {
		return false, err
	}
	rpcCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	resp, err := a.client.IsConverged(rpcCtx, &kaiv1alpha1.IsConvergedRequest{
		Target:     clusterName,
		Version:    version,
		Parameters: a.parametersWithAppKey(appKey),
	})
	if err != nil {
		return false, fmt.Errorf("check convergence via actuator plugin %q: %w", a.name, err)
	}
	return resp.GetConverged(), nil
}

// Rollback instructs the external plugin to roll back to a previous version.
func (a *ActuatorAdapter) Rollback(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, previousVersion, appKey string) error {
	clusterName, err := clusterName(cluster)
	if err != nil {
		return err
	}
	rpcCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	resp, err := a.client.Rollback(rpcCtx, &kaiv1alpha1.RollbackRequest{
		Target:          clusterName,
		PreviousVersion: previousVersion,
		Parameters:      a.parametersWithAppKey(appKey),
	})
	if err != nil {
		return fmt.Errorf("rollback via actuator plugin %q: %w", a.name, err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("actuator plugin %q rejected rollback: %s", a.name, resp.GetMessage())
	}
	return nil
}

// ApplyDelta applies changed app/version entries one at a time through the KAI v1alpha1 Apply RPC.
func (a *ActuatorAdapter) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("target cluster is required")
	}
	applied := 0
	for appKey, version := range req.DesiredVersions {
		current := ""
		if req.Cluster.Status.CurrentVersions != nil {
			current = req.Cluster.Status.CurrentVersions[appKey]
		}
		if current == version {
			continue
		}
		if err := a.Apply(ctx, actuator.ApplyRequest{Cluster: req.Cluster, Version: version, PreviousVersion: current, AppKey: appKey}); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// IsAllConverged returns true only when every desired app/version has converged.
func (a *ActuatorAdapter) IsAllConverged(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, desiredVersions map[string]string) (bool, error) {
	for appKey, version := range desiredVersions {
		converged, err := a.IsConverged(ctx, cluster, version, appKey)
		if err != nil || !converged {
			return converged, err
		}
	}
	return true, nil
}

func (a *ActuatorAdapter) parametersWithAppKey(appKey string) map[string]string {
	params := copyParameters(a.parameters)
	if appKey != "" {
		params[appKeyParam] = appKey
	}
	return params
}

func clusterName(cluster *kaprov1alpha1.MemberCluster) (string, error) {
	if cluster == nil {
		return "", fmt.Errorf("target cluster is required")
	}
	if cluster.Name == "" {
		return "", fmt.Errorf("target cluster name is required")
	}
	return cluster.Name, nil
}
