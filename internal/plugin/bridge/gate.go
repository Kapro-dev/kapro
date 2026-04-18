package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/grpc"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

// gateRequest is what the bridge sends to the remote plugin's GateService.
// We inline the minimal fields a plugin needs, rather than serialising entire
// Kubernetes objects — keeps the wire format stable across CRD changes.
type gateRequest struct {
	PromotionName   string            `json:"promotion_name"`
	EnvironmentName string            `json:"environment_name"`
	ReleaseName     string            `json:"release_name"`
	PolicyName      string            `json:"policy_name"`
	MetricIndex     int               `json:"metric_index"`
	MetricConfig    map[string]string `json:"metric_config,omitempty"`
	TimeoutSeconds  int64             `json:"timeout_seconds,omitempty"`
}

// gateResponse mirrors GateService.EvaluateResponse.
type gateResponse struct {
	Passed     bool   `json:"passed"`
	Message    string `json:"message,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
}

// GateBridge implements pkggate.Gate by forwarding calls to a remote plugin
// over gRPC using a JSON wire format.
type GateBridge struct {
	// PluginName is the PluginRegistration resource name — used for logging.
	PluginName string
	// Conn is the active gRPC connection managed by plugin.Reconciler.
	Conn *grpc.ClientConn
	// Timeout is the per-call deadline; defaults to 30s.
	Timeout time.Duration
}

const gateServicePath = "/kapro.v1alpha1.GateService/Evaluate"

// Evaluate implements pkggate.Gate.
func (b *GateBridge) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	timeout := b.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Extract stable scalar fields for the wire request.
	metricCfg := metricConfigFrom(req.Policy, req.MetricIndex)
	payload := gateRequest{
		PromotionName:  req.Promotion.Name,
		PolicyName:     req.Policy.Name,
		MetricIndex:    req.MetricIndex,
		MetricConfig:   metricCfg,
		TimeoutSeconds: int64(timeout.Seconds()),
	}
	// Attach environment name from the promotion's target reference.
	if req.Promotion != nil {
		payload.EnvironmentName = req.Promotion.Spec.EnvironmentRef
		payload.ReleaseName = req.Promotion.Spec.ReleaseRef
	}

	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("plugin %s: marshal request: %w", b.PluginName, err)
	}

	var respBytes []byte
	if err := b.Conn.Invoke(ctx, gateServicePath, &rawMessage{reqBytes}, &rawMessage{&respBytes}); err != nil {
		return pkggate.Result{}, fmt.Errorf("plugin %s: rpc Evaluate: %w", b.PluginName, err)
	}

	var resp gateResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return pkggate.Result{}, fmt.Errorf("plugin %s: unmarshal response: %w", b.PluginName, err)
	}

	return pkggate.Result{
		Passed:     resp.Passed,
		Message:    resp.Message,
		RetryAfter: resp.RetryAfter,
	}, nil
}

// metricConfigFrom extracts the config map for a specific metric in a policy.
// Returns nil if the index is out of range.
func metricConfigFrom(policy *kaprov1alpha1.PromotionPolicy, idx int) map[string]string {
	if policy == nil {
		return nil
	}
	metrics := policy.Spec.Gate.Metrics
	if idx < 0 || idx >= len(metrics) {
		return nil
	}
	m := metrics[idx]
	// Flatten provider + query into a stable map so plugins don't import the CRD type.
	return map[string]string{
		"provider": m.Provider,
		"query":    m.Query,
		"window":   m.Window,
	}
}

// rawMessage is a minimal grpc.Codec-compatible wrapper that lets us pass
// raw []byte as a gRPC message without proto codegen.
// Registered under the "json" subtype in codec.go.
type rawMessage struct {
	data interface{} // *[]byte for recv, []byte for send
}
