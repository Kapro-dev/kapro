// Package adapter wires ready PluginRegistration endpoints into Kapro's in-process registries.
package adapter

import (
	"context"
	"fmt"
	"os"
	"time"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/plugin/transport"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/planner"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// EnableEnv gates opt-in plugin runtime registration.
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

type closeable interface {
	Close() error
}

// RegisterReady registers ready, generation-fresh PluginRegistration objects.
func (r Registrar) RegisterReady(ctx context.Context, c client.Reader, actuatorReg *actuator.Registry, gateReg *gate.Registry, plannerFramework *planner.Framework) (int, error) {
	var list kaprov1alpha2.PluginList
	if err := c.List(ctx, &list); err != nil {
		return 0, fmt.Errorf("list plugin registrations: %w", err)
	}

	registered := 0
	registeredByType := map[kaprov1alpha2.PluginType]int{
		kaprov1alpha2.PluginTypeActuator: 0,
		kaprov1alpha2.PluginTypeGate:     0,
		kaprov1alpha2.PluginTypePlanner:  0,
	}
	defer func() {
		for pluginType, count := range registeredByType {
			kaprometrics.PluginRuntimeRegistered.WithLabelValues(string(pluginType)).Set(float64(count))
		}
	}()
	for _, reg := range list.Items {
		if !isReadyForRuntime(reg) {
			continue
		}
		if err := r.RegisterOne(ctx, c, reg, actuatorReg, gateReg, plannerFramework); err != nil {
			return registered, err
		}
		registered++
		registeredByType[reg.Spec.Type]++
	}
	return registered, nil
}

// RegisterOne registers or replaces a single ready PluginRegistration.
func (r Registrar) RegisterOne(ctx context.Context, c client.Reader, reg kaprov1alpha2.Plugin, actuatorReg *actuator.Registry, gateReg *gate.Registry, plannerFramework *planner.Framework) error {
	if !isReadyForRuntime(reg) {
		return nil
	}
	if err := validateRegistration(reg); err != nil {
		return err
	}
	switch reg.Spec.Type {
	case kaprov1alpha2.PluginTypeActuator:
		if actuatorReg == nil {
			return fmt.Errorf("actuator registry is nil")
		}
		conn, err := r.dial(ctx, c, reg)
		if err != nil {
			return fmt.Errorf("dial actuator plugin %q: %w", reg.Name, err)
		}
		adapter, err := NewActuatorAdapter(reg, kaiv1alpha1.NewActuatorServiceClient(conn))
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("create actuator plugin adapter for registration %q: %w", reg.Name, err)
		}
		adapter.conn = conn
		old, err := actuatorReg.UpsertRegistration(actuator.Registration{
			Name:         reg.Spec.Name,
			Capabilities: adapter.Capabilities(),
			Actuator:     adapter,
		})
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("register actuator plugin adapter for registration %q: %w", reg.Name, err)
		}
		closeRuntime(old)
	case kaprov1alpha2.PluginTypeGate:
		if gateReg == nil {
			return fmt.Errorf("gate registry is nil")
		}
		conn, err := r.dial(ctx, c, reg)
		if err != nil {
			return fmt.Errorf("dial gate plugin %q: %w", reg.Name, err)
		}
		adapter, err := NewGateAdapter(reg, kgiv1alpha1.NewGateServiceClient(conn))
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("create gate plugin adapter for registration %q: %w", reg.Name, err)
		}
		adapter.conn = conn
		closeRuntime(gateReg.Upsert(reg.Spec.Name, adapter))
	case kaprov1alpha2.PluginTypePlanner:
		if plannerFramework == nil {
			return fmt.Errorf("planner framework is nil")
		}
		conn, err := r.dial(ctx, c, reg)
		if err != nil {
			return fmt.Errorf("dial planner plugin %q: %w", reg.Name, err)
		}
		adapter, err := NewPlannerAdapter(reg, kpiv1alpha1.NewPlannerServiceClient(conn))
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("create planner plugin adapter for registration %q: %w", reg.Name, err)
		}
		adapter.conn = conn
		closeRuntime(plannerFramework.Upsert(adapter))
	default:
		return fmt.Errorf("unsupported plugin type %q", reg.Spec.Type)
	}
	return nil
}

// UnregisterOne removes a runtime plugin adapter. It is used when a previously
// ready registration becomes stale, incompatible, or deleted.
func (r Registrar) UnregisterOne(reg kaprov1alpha2.Plugin, actuatorReg *actuator.Registry, gateReg *gate.Registry, plannerFramework *planner.Framework) {
	switch reg.Spec.Type {
	case kaprov1alpha2.PluginTypeActuator:
		if actuatorReg != nil {
			old, _ := actuatorReg.Unregister(reg.Spec.Name)
			closeRuntime(old)
		}
	case kaprov1alpha2.PluginTypeGate:
		if gateReg != nil {
			old, _ := gateReg.Unregister(reg.Spec.Name)
			closeRuntime(old)
		}
	case kaprov1alpha2.PluginTypePlanner:
		if plannerFramework != nil {
			old, _ := plannerFramework.Unregister(reg.Spec.Name)
			closeRuntime(old)
		}
	}
}

func (r Registrar) dial(ctx context.Context, c client.Reader, reg kaprov1alpha2.Plugin) (*grpc.ClientConn, error) {
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
		return nil, fmt.Errorf("build dial options for endpoint %q: %w", reg.Spec.Endpoint, err)
	}
	opts = append(opts, grpc.WithBlock()) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	opts = append(opts, r.DialOptions...)
	conn, err := grpc.DialContext(dialCtx, reg.Spec.Endpoint, opts...) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	if err != nil {
		return nil, fmt.Errorf("connect to endpoint %q within %s: %w", reg.Spec.Endpoint, timeout, err)
	}
	return conn, nil
}

func isReadyForRuntime(reg kaprov1alpha2.Plugin) bool {
	return reg.Status.Ready && reg.Status.ObservedGeneration == reg.Generation
}

func validateRegistration(reg kaprov1alpha2.Plugin) error {
	if reg.Spec.Type != kaprov1alpha2.PluginTypeActuator &&
		reg.Spec.Type != kaprov1alpha2.PluginTypeGate &&
		reg.Spec.Type != kaprov1alpha2.PluginTypePlanner {
		return fmt.Errorf("unsupported plugin type %q", reg.Spec.Type)
	}
	if reg.Spec.Protocol != "" && reg.Spec.Protocol != kaprov1alpha2.PluginProtocolGRPC {
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

func closeRuntime(old any) {
	if c, ok := old.(closeable); ok {
		_ = c.Close()
	}
}

func timeoutFor(reg kaprov1alpha2.Plugin) (time.Duration, error) {
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

func observeRuntimeCall(pluginType kaprov1alpha2.PluginType, name, method, result string, start time.Time) {
	labels := []string{string(pluginType), name, method, result}
	kaprometrics.PluginRuntimeCalls.WithLabelValues(labels...).Inc()
	kaprometrics.PluginRuntimeCallDuration.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
}
