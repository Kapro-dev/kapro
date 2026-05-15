package adapter

import (
	"context"
	"fmt"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/gate"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/grpc"
)

// GateAdapter adapts a KGI gRPC plugin to pkg/gate.Gate.
type GateAdapter struct {
	name       string
	endpoint   string
	client     kgiv1alpha1.GateServiceClient
	timeout    time.Duration
	parameters map[string]string
	conn       *grpc.ClientConn
}

// NewGateAdapter returns a gate adapter backed by a KGI client.
func NewGateAdapter(reg kaprov1alpha1.PluginRegistration, client kgiv1alpha1.GateServiceClient) (*GateAdapter, error) {
	if reg.Spec.Type != kaprov1alpha1.PluginTypeGate {
		return nil, fmt.Errorf("plugin %q is %q, expected %q", reg.Name, reg.Spec.Type, kaprov1alpha1.PluginTypeGate)
	}
	if client == nil {
		return nil, fmt.Errorf("gate plugin client is nil")
	}
	if err := validateRegistration(reg); err != nil {
		return nil, err
	}
	timeout, err := timeoutFor(reg)
	if err != nil {
		return nil, err
	}
	return &GateAdapter{
		name:       reg.Spec.Name,
		endpoint:   reg.Spec.Endpoint,
		client:     client,
		timeout:    timeout,
		parameters: copyParameters(reg.Spec.Parameters),
	}, nil
}

// Evaluate asks the external plugin whether this target may advance.
func (g *GateAdapter) Evaluate(ctx context.Context, req gate.Request) (gate.Result, error) {
	start := time.Now()
	result := "success"
	defer func() { observeRuntimeCall(kaprov1alpha1.PluginTypeGate, g.name, "Evaluate", result, start) }()

	if req.Context == nil {
		result = "error"
		return gate.Result{}, fmt.Errorf("gate context is required")
	}

	rpcCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	resp, err := g.client.Evaluate(rpcCtx, &kgiv1alpha1.EvaluateRequest{
		Release:    req.Context.ReleaseRef,
		Target:     req.Context.Target,
		Pipeline:   req.Context.Pipeline,
		Stage:      req.Context.Stage,
		Version:    req.Context.Version,
		Gate:       gateName(g.name, req.Template),
		Parameters: mergeParameters(g.parameters, req.Args),
	})
	if err != nil {
		result = "error"
		return gate.Result{}, fmt.Errorf("gate plugin %q Evaluate RPC to %q failed for target %q stage %q: %w", g.name, g.endpoint, req.Context.Target, req.Context.Stage, err)
	}
	phase, err := mapGatePhase(resp.GetPhase())
	if err != nil {
		result = "invalid_response"
		return gate.Result{}, fmt.Errorf("gate plugin %q Evaluate returned invalid phase for target %q stage %q: %w", g.name, req.Context.Target, req.Context.Stage, err)
	}
	return gate.Result{Phase: phase, Message: resp.GetMessage()}, nil
}

func gateName(defaultName string, tmpl *kaprov1alpha1.GateTemplateSpec) string {
	if tmpl != nil && tmpl.Name != "" {
		return tmpl.Name
	}
	return defaultName
}

func mapGatePhase(phase kgiv1alpha1.GatePhase) (kaprov1alpha1.GatePhase, error) {
	switch phase {
	case kgiv1alpha1.GatePhase_GATE_PHASE_PASSED:
		return kaprov1alpha1.GatePhasePassed, nil
	case kgiv1alpha1.GatePhase_GATE_PHASE_FAILED:
		return kaprov1alpha1.GatePhaseFailed, nil
	case kgiv1alpha1.GatePhase_GATE_PHASE_RUNNING:
		return kaprov1alpha1.GatePhaseRunning, nil
	case kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE:
		return kaprov1alpha1.GatePhaseInconclusive, nil
	default:
		return "", fmt.Errorf("unsupported gate phase %s", phase.String())
	}
}
