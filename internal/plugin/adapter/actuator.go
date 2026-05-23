package adapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/actuator"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"

	"google.golang.org/grpc"
)

// ActuatorAdapter adapts a KAI gRPC plugin to pkg/actuator.Actuator.
type ActuatorAdapter struct {
	name         string
	endpoint     string
	client       kaiv1alpha1.ActuatorServiceClient
	timeout      time.Duration
	parameters   map[string]string
	capabilities actuator.Capabilities
	conn         *grpc.ClientConn
}

// NewActuatorAdapter returns an actuator adapter backed by a KAI client.
func NewActuatorAdapter(reg kaprov1alpha2.Plugin, client kaiv1alpha1.ActuatorServiceClient) (*ActuatorAdapter, error) {
	if reg.Spec.Type != kaprov1alpha2.PluginTypeActuator {
		return nil, fmt.Errorf("plugin %q is %q, expected %q", reg.Name, reg.Spec.Type, kaprov1alpha2.PluginTypeActuator)
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
		name:         reg.Spec.Name,
		endpoint:     reg.Spec.Endpoint,
		client:       client,
		timeout:      timeout,
		parameters:   copyParameters(reg.Spec.Parameters),
		capabilities: actuatorCapabilitiesForPlugin(reg).Normalize(),
	}, nil
}

func (a *ActuatorAdapter) Capabilities() actuator.Capabilities {
	if a == nil {
		return actuator.Capabilities{}.Normalize()
	}
	return a.capabilities.Normalize()
}

// Close closes the underlying plugin connection when this adapter owns one.
func (a *ActuatorAdapter) Close() error {
	if a == nil || a.conn == nil {
		return nil
	}
	return a.conn.Close()
}

// Apply instructs the external plugin to apply a version to one target.
func (a *ActuatorAdapter) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	start := time.Now()
	result := "success"
	defer func() { observeRuntimeCall(kaprov1alpha2.PluginTypeActuator, a.name, "Apply", result, start) }()

	clusterName, err := clusterName(req.Cluster)
	if err != nil {
		result = "error"
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
		result = "error"
		return fmt.Errorf("actuator plugin %q Apply RPC to %q failed: %w", a.name, a.endpoint, err)
	}
	if !resp.GetAccepted() {
		result = "rejected"
		return fmt.Errorf("actuator plugin %q rejected Apply for target %q version %q: %s", a.name, clusterName, req.Version, responseMessage(resp.GetMessage()))
	}
	return nil
}

// IsConverged asks the external plugin whether a target has converged.
func (a *ActuatorAdapter) IsConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, version, appKey string) (bool, error) {
	start := time.Now()
	result := "success"
	defer func() { observeRuntimeCall(kaprov1alpha2.PluginTypeActuator, a.name, "IsConverged", result, start) }()

	clusterName, err := clusterName(cluster)
	if err != nil {
		result = "error"
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
		result = "error"
		return false, fmt.Errorf("actuator plugin %q IsConverged RPC to %q failed for target %q version %q: %w", a.name, a.endpoint, clusterName, version, err)
	}
	return resp.GetConverged(), nil
}

// Rollback instructs the external plugin to roll back to a previous version.
func (a *ActuatorAdapter) Rollback(ctx context.Context, cluster *kaprov1alpha2.Cluster, previousVersion, appKey string) error {
	start := time.Now()
	result := "success"
	defer func() { observeRuntimeCall(kaprov1alpha2.PluginTypeActuator, a.name, "Rollback", result, start) }()

	clusterName, err := clusterName(cluster)
	if err != nil {
		result = "error"
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
		result = "error"
		return fmt.Errorf("actuator plugin %q Rollback RPC to %q failed for target %q previous version %q: %w", a.name, a.endpoint, clusterName, previousVersion, err)
	}
	if !resp.GetAccepted() {
		result = "rejected"
		return fmt.Errorf("actuator plugin %q rejected Rollback for target %q previous version %q: %s", a.name, clusterName, previousVersion, responseMessage(resp.GetMessage()))
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
func (a *ActuatorAdapter) IsAllConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) (bool, error) {
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

func clusterName(cluster *kaprov1alpha2.Cluster) (string, error) {
	if cluster == nil {
		return "", fmt.Errorf("target cluster is required")
	}
	if cluster.Name == "" {
		return "", fmt.Errorf("target cluster name is required")
	}
	return cluster.Name, nil
}

func responseMessage(message string) string {
	if message == "" {
		return "plugin returned no message"
	}
	return message
}

func actuatorCapabilitiesForPlugin(reg kaprov1alpha2.Plugin) actuator.Capabilities {
	caps := actuator.Capabilities{
		ContractVersion: reg.Status.ContractVersion,
		Adapter:         reg.Spec.Name,
		Runtime:         kaprov1alpha2.BackendRuntimeBoth,
	}
	for _, capability := range reg.Status.Capabilities {
		capability = strings.ToLower(capability)
		switch {
		case strings.Contains(capability, "apply"):
			caps.SupportsApply = true
		case strings.Contains(capability, "rollback"):
			caps.SupportsRollback = true
		case strings.Contains(capability, "convergence") || strings.Contains(capability, "observe"):
			caps.SupportsObserve = true
			caps.SupportsConvergence = true
		case strings.Contains(capability, "delta"):
			caps.SupportsDelta = true
		case strings.Contains(capability, "backendobject") || strings.Contains(capability, "backend-object"):
			caps.SupportsBackendObjects = true
		case strings.Contains(capability, "dry-run") || strings.Contains(capability, "dryrun"):
			caps.SupportsDryRun = true
		}
	}
	if caps.SupportsApply {
		// KAI v1alpha1 exposes single-artifact Apply; this adapter implements
		// ApplyDelta by issuing Apply once per changed app key.
		caps.SupportsDelta = true
	}
	return caps
}
