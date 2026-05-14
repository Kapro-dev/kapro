// Package adapter wires ready PluginRegistration endpoints into Kapro's in-process registries.
package adapter

import (
	"context"
	"fmt"
	"os"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/plugin/transport"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/grpc"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// EnableEnv gates startup-time plugin runtime registration.
	EnableEnv = "KAPRO_ENABLE_PLUGIN_GATEWAY"

	defaultTimeout = 10 * time.Second
	appKeyParam    = "appKey"
)

// EnabledFromEnv returns true when runtime plugin registration is enabled.
func EnabledFromEnv() bool {
	return os.Getenv(EnableEnv) == "true"
}

// Registrar loads ready PluginRegistration objects and registers runtime adapters.
type Registrar struct {
	DialOptions []grpc.DialOption
}

// RegisterReady registers ready, generation-fresh PluginRegistration objects once.
func (r Registrar) RegisterReady(ctx context.Context, c client.Reader, actuatorReg *actuator.Registry, gateReg *gate.Registry) (int, error) {
	var list kaprov1alpha1.PluginRegistrationList
	if err := c.List(ctx, &list); err != nil {
		return 0, fmt.Errorf("list plugin registrations: %w", err)
	}

	registered := 0
	for _, reg := range list.Items {
		if !isReadyForRuntime(reg) {
			continue
		}
		switch reg.Spec.Type {
		case kaprov1alpha1.PluginTypeActuator:
			if actuatorReg == nil {
				return registered, fmt.Errorf("actuator registry is nil")
			}
			conn, err := r.dial(ctx, c, reg)
			if err != nil {
				return registered, fmt.Errorf("dial actuator plugin %q: %w", reg.Name, err)
			}
			adapter, err := NewActuatorAdapter(reg, kaiv1alpha1.NewActuatorServiceClient(conn))
			if err != nil {
				_ = conn.Close()
				return registered, err
			}
			adapter.conn = conn
			if err := actuatorReg.Register(reg.Spec.Name, adapter); err != nil {
				_ = conn.Close()
				return registered, fmt.Errorf("register actuator plugin %q: %w", reg.Spec.Name, err)
			}
			registered++
		case kaprov1alpha1.PluginTypeGate:
			if gateReg == nil {
				return registered, fmt.Errorf("gate registry is nil")
			}
			conn, err := r.dial(ctx, c, reg)
			if err != nil {
				return registered, fmt.Errorf("dial gate plugin %q: %w", reg.Name, err)
			}
			adapter, err := NewGateAdapter(reg, kgiv1alpha1.NewGateServiceClient(conn))
			if err != nil {
				_ = conn.Close()
				return registered, err
			}
			adapter.conn = conn
			if err := gateReg.Register(reg.Spec.Name, adapter); err != nil {
				_ = conn.Close()
				return registered, fmt.Errorf("register gate plugin %q: %w", reg.Spec.Name, err)
			}
			registered++
		case kaprov1alpha1.PluginTypePlanner:
			// Planner runtime dispatch is not wired yet. The registration
			// controller still probes planner plugins and records readiness.
			continue
		}
	}
	return registered, nil
}

func (r Registrar) dial(ctx context.Context, c client.Reader, reg kaprov1alpha1.PluginRegistration) (*grpc.ClientConn, error) {
	if err := validateRegistration(reg); err != nil {
		return nil, err
	}
	timeout, err := timeoutFor(reg)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts, err := transport.DialOptions(dialCtx, c, reg)
	if err != nil {
		return nil, err
	}
	opts = append(opts, grpc.WithBlock()) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	opts = append(opts, r.DialOptions...)
	conn, err := grpc.DialContext(dialCtx, reg.Spec.Endpoint, opts...) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func isReadyForRuntime(reg kaprov1alpha1.PluginRegistration) bool {
	return reg.Status.Ready && reg.Status.ObservedGeneration == reg.Generation
}

func validateRegistration(reg kaprov1alpha1.PluginRegistration) error {
	if reg.Spec.Type != kaprov1alpha1.PluginTypeActuator && reg.Spec.Type != kaprov1alpha1.PluginTypeGate {
		return fmt.Errorf("unsupported plugin type %q", reg.Spec.Type)
	}
	if reg.Spec.Protocol != "" && reg.Spec.Protocol != kaprov1alpha1.PluginProtocolGRPC {
		return fmt.Errorf("unsupported plugin protocol %q", reg.Spec.Protocol)
	}
	if reg.Spec.Name == "" {
		return fmt.Errorf("plugin registry name is required")
	}
	if reg.Spec.Endpoint == "" {
		return fmt.Errorf("plugin endpoint is required")
	}
	return nil
}

func timeoutFor(reg kaprov1alpha1.PluginRegistration) (time.Duration, error) {
	if reg.Spec.Timeout == "" {
		return defaultTimeout, nil
	}
	timeout, err := time.ParseDuration(reg.Spec.Timeout)
	if err != nil {
		return 0, fmt.Errorf("parse timeout %q: %w", reg.Spec.Timeout, err)
	}
	return timeout, nil
}

func copyParameters(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeParameters(base map[string]string, overlays ...map[string]string) map[string]string {
	out := copyParameters(base)
	for _, overlay := range overlays {
		for k, v := range overlay {
			out[k] = v
		}
	}
	return out
}
