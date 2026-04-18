package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/grpc"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkgactuator "kapro.io/kapro/pkg/actuator"
)

// applyRequest mirrors ActuatorService.Apply proto request.
type applyRequest struct {
	EnvironmentName string `json:"environment_name"`
	ReleaseName     string `json:"release_name"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version,omitempty"`
}

// applyResponse mirrors ActuatorService.ApplyResponse.
type applyResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// convergedRequest mirrors ActuatorService.IsConverged proto request.
type convergedRequest struct {
	EnvironmentName string `json:"environment_name"`
	Version         string `json:"version"`
}

// convergedResponse mirrors ActuatorService.IsConvergedResponse.
type convergedResponse struct {
	Converged bool   `json:"converged"`
	Reason    string `json:"reason,omitempty"`
}

// rollbackRequest mirrors ActuatorService.Rollback proto request.
type rollbackRequest struct {
	EnvironmentName string `json:"environment_name"`
	Version         string `json:"version"`
}

// rollbackResponse mirrors ActuatorService.RollbackResponse.
type rollbackResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

const (
	actuatorApplyPath     = "/kapro.v1alpha1.ActuatorService/Apply"
	actuatorConvergedPath = "/kapro.v1alpha1.ActuatorService/IsConverged"
	actuatorRollbackPath  = "/kapro.v1alpha1.ActuatorService/Rollback"
)

// ActuatorBridge implements pkgactuator.Actuator by forwarding calls to a
// remote plugin over gRPC using a JSON wire format.
type ActuatorBridge struct {
	// PluginName is the PluginRegistration resource name — used for logging.
	PluginName string
	// Conn is the active gRPC connection managed by plugin.Reconciler.
	Conn *grpc.ClientConn
	// Timeout is the per-call deadline; defaults to 60s.
	Timeout time.Duration
}

func (b *ActuatorBridge) callTimeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return 60 * time.Second
}

// Apply implements pkgactuator.Actuator.
func (b *ActuatorBridge) Apply(ctx context.Context, req pkgactuator.ApplyRequest) error {
	ctx, cancel := context.WithTimeout(ctx, b.callTimeout())
	defer cancel()

	payload := applyRequest{
		Version:         req.Version,
		PreviousVersion: req.PreviousVersion,
	}
	if req.Environment != nil {
		payload.EnvironmentName = req.Environment.Name
	}

	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("plugin %s: marshal Apply: %w", b.PluginName, err)
	}

	var respBytes []byte
	if err := b.Conn.Invoke(ctx, actuatorApplyPath, &rawMessage{reqBytes}, &rawMessage{&respBytes}); err != nil {
		return fmt.Errorf("plugin %s: rpc Apply: %w", b.PluginName, err)
	}

	var resp applyResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("plugin %s: unmarshal Apply response: %w", b.PluginName, err)
	}
	if !resp.Accepted {
		return fmt.Errorf("plugin %s: Apply rejected: %s", b.PluginName, resp.Message)
	}
	return nil
}

// IsConverged implements pkgactuator.Actuator.
func (b *ActuatorBridge) IsConverged(ctx context.Context, env *kaprov1alpha1.Environment, version string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, b.callTimeout())
	defer cancel()

	payload := convergedRequest{Version: version}
	if env != nil {
		payload.EnvironmentName = env.Name
	}

	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("plugin %s: marshal IsConverged: %w", b.PluginName, err)
	}

	var respBytes []byte
	if err := b.Conn.Invoke(ctx, actuatorConvergedPath, &rawMessage{reqBytes}, &rawMessage{&respBytes}); err != nil {
		return false, fmt.Errorf("plugin %s: rpc IsConverged: %w", b.PluginName, err)
	}

	var resp convergedResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return false, fmt.Errorf("plugin %s: unmarshal IsConverged response: %w", b.PluginName, err)
	}
	return resp.Converged, nil
}

// Rollback implements pkgactuator.Actuator.
func (b *ActuatorBridge) Rollback(ctx context.Context, env *kaprov1alpha1.Environment, previousVersion string) error {
	ctx, cancel := context.WithTimeout(ctx, b.callTimeout())
	defer cancel()

	payload := rollbackRequest{Version: previousVersion}
	if env != nil {
		payload.EnvironmentName = env.Name
	}

	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("plugin %s: marshal Rollback: %w", b.PluginName, err)
	}

	var respBytes []byte
	if err := b.Conn.Invoke(ctx, actuatorRollbackPath, &rawMessage{reqBytes}, &rawMessage{&respBytes}); err != nil {
		return fmt.Errorf("plugin %s: rpc Rollback: %w", b.PluginName, err)
	}

	var resp rollbackResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("plugin %s: unmarshal Rollback response: %w", b.PluginName, err)
	}
	if !resp.Accepted {
		return fmt.Errorf("plugin %s: Rollback rejected: %s", b.PluginName, resp.Message)
	}
	return nil
}
