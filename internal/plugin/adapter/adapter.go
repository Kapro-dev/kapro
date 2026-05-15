// Package adapter wires ready PluginRegistration endpoints into Kapro's in-process registries.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/plugin/transport"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/types"
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
	return NewRuntimeReloader(actuatorReg, gateReg, r.DialOptions...).SyncReady(ctx, c)
}

// RuntimeReloader reconciles ready PluginRegistration objects into runtime registries.
type RuntimeReloader struct {
	DialOptions []grpc.DialOption

	actuatorReg *actuator.Registry
	gateReg     *gate.Registry

	mu       sync.Mutex
	byObject map[types.NamespacedName]runtimeRegistration
	byType   map[kaprov1alpha1.PluginType]int
}

type runtimeRegistration struct {
	PluginType  kaprov1alpha1.PluginType
	Name        string
	Fingerprint string
	Actuator    actuator.Actuator
	Gate        gate.Gate
	Closer      io.Closer
}

// NewRuntimeReloader returns a runtime plugin reloader backed by the supplied registries.
func NewRuntimeReloader(actuatorReg *actuator.Registry, gateReg *gate.Registry, dialOptions ...grpc.DialOption) *RuntimeReloader {
	return &RuntimeReloader{
		DialOptions: dialOptions,
		actuatorReg: actuatorReg,
		gateReg:     gateReg,
		byObject:    map[types.NamespacedName]runtimeRegistration{},
		byType:      map[kaprov1alpha1.PluginType]int{},
	}
}

// SyncReady reconciles all PluginRegistrations visible to the reader and removes cached registrations no longer present.
func (r *RuntimeReloader) SyncReady(ctx context.Context, c client.Reader) (int, error) {
	var list kaprov1alpha1.PluginRegistrationList
	if err := c.List(ctx, &list); err != nil {
		return 0, fmt.Errorf("list plugin registrations: %w", err)
	}

	registered := 0
	seen := make(map[types.NamespacedName]struct{}, len(list.Items))
	for _, reg := range list.Items {
		key := objectKey(reg)
		seen[key] = struct{}{}
		changed, err := r.Reconcile(ctx, c, reg)
		if err != nil {
			return registered, err
		}
		if changed && isReadyForRuntime(reg) && supportsRuntime(reg.Spec.Type) {
			registered++
		}
	}
	r.mu.Lock()
	for key := range r.byObject {
		if _, ok := seen[key]; !ok {
			r.unregisterLocked(key)
		}
	}
	r.observeLocked()
	r.mu.Unlock()
	return registered, nil
}

// Reconcile registers, updates, or unregisters one PluginRegistration.
func (r *RuntimeReloader) Reconcile(ctx context.Context, c client.Reader, reg kaprov1alpha1.PluginRegistration) (bool, error) {
	key := objectKey(reg)
	if !isReadyForRuntime(reg) || !supportsRuntime(reg.Spec.Type) {
		r.Unregister(key)
		return false, nil
	}
	fingerprint, err := registrationFingerprint(reg)
	if err != nil {
		return false, err
	}

	r.mu.Lock()
	current, exists := r.byObject[key]
	if exists && current.PluginType == reg.Spec.Type && current.Name == reg.Spec.Name && current.Fingerprint == fingerprint {
		r.observeLocked()
		r.mu.Unlock()
		return false, nil
	}
	r.mu.Unlock()

	conn, err := r.dial(ctx, c, reg)
	if err != nil {
		return false, fmt.Errorf("dial %s plugin %q: %w", reg.Spec.Type, reg.Name, err)
	}

	record, err := r.registerRuntime(reg, fingerprint, conn)
	if err != nil {
		_ = conn.Close()
		return false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if current, exists := r.byObject[key]; exists && current.PluginType == reg.Spec.Type && current.Name == reg.Spec.Name && current.Fingerprint == fingerprint {
		_ = record.Closer.Close()
		r.observeLocked()
		return false, nil
	}

	if old, ok := r.byObject[key]; ok {
		if old.Name == record.Name && old.PluginType == record.PluginType {
			if err := r.replaceLocked(record); err != nil {
				_ = record.Closer.Close()
				r.observeLocked()
				return false, err
			}
			if old.Closer != nil {
				_ = old.Closer.Close()
			}
			r.byObject[key] = record
			r.observeLocked()
			return true, nil
		}
		if err := r.registerLocked(record); err != nil {
			_ = record.Closer.Close()
			r.observeLocked()
			return false, err
		}
		r.unregisterLocked(key)
		r.byObject[key] = record
		r.byType[record.PluginType]++
		r.observeLocked()
		return true, nil
	}
	if err := r.registerLocked(record); err != nil {
		_ = record.Closer.Close()
		r.observeLocked()
		return false, err
	}
	r.byObject[key] = record
	r.byType[reg.Spec.Type]++
	r.observeLocked()
	return true, nil
}

// Unregister removes a cached runtime registration by object key.
func (r *RuntimeReloader) Unregister(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unregisterLocked(key)
	r.observeLocked()
}

func (r *RuntimeReloader) registerRuntime(reg kaprov1alpha1.PluginRegistration, fingerprint string, conn *grpc.ClientConn) (runtimeRegistration, error) {
	record := runtimeRegistration{
		PluginType:  reg.Spec.Type,
		Name:        reg.Spec.Name,
		Fingerprint: fingerprint,
		Closer:      conn,
	}
	switch reg.Spec.Type {
	case kaprov1alpha1.PluginTypeActuator:
		adapter, err := NewActuatorAdapter(reg, kaiv1alpha1.NewActuatorServiceClient(conn))
		if err != nil {
			return runtimeRegistration{}, fmt.Errorf("create actuator plugin adapter for registration %q: %w", reg.Name, err)
		}
		adapter.conn = conn
		record.Actuator = adapter
		record.Closer = conn
	case kaprov1alpha1.PluginTypeGate:
		adapter, err := NewGateAdapter(reg, kgiv1alpha1.NewGateServiceClient(conn))
		if err != nil {
			return runtimeRegistration{}, fmt.Errorf("create gate plugin adapter for registration %q: %w", reg.Name, err)
		}
		adapter.conn = conn
		record.Gate = adapter
		record.Closer = conn
	}
	return record, nil
}

func (r *RuntimeReloader) registerLocked(record runtimeRegistration) error {
	switch record.PluginType {
	case kaprov1alpha1.PluginTypeActuator:
		if r.actuatorReg == nil {
			return fmt.Errorf("actuator registry is nil")
		}
		return r.actuatorReg.Register(record.Name, record.Actuator)
	case kaprov1alpha1.PluginTypeGate:
		if r.gateReg == nil {
			return fmt.Errorf("gate registry is nil")
		}
		return r.gateReg.Register(record.Name, record.Gate)
	default:
		return fmt.Errorf("unsupported plugin type %q", record.PluginType)
	}
}

func (r *RuntimeReloader) replaceLocked(record runtimeRegistration) error {
	switch record.PluginType {
	case kaprov1alpha1.PluginTypeActuator:
		if r.actuatorReg == nil {
			return fmt.Errorf("actuator registry is nil")
		}
		return r.actuatorReg.Replace(record.Name, record.Actuator)
	case kaprov1alpha1.PluginTypeGate:
		if r.gateReg == nil {
			return fmt.Errorf("gate registry is nil")
		}
		return r.gateReg.Replace(record.Name, record.Gate)
	default:
		return fmt.Errorf("unsupported plugin type %q", record.PluginType)
	}
}

func (r *RuntimeReloader) unregisterLocked(key types.NamespacedName) {
	record, ok := r.byObject[key]
	if !ok {
		return
	}
	switch record.PluginType {
	case kaprov1alpha1.PluginTypeActuator:
		if r.actuatorReg != nil {
			r.actuatorReg.Unregister(record.Name)
		}
	case kaprov1alpha1.PluginTypeGate:
		if r.gateReg != nil {
			r.gateReg.Unregister(record.Name)
		}
	}
	if record.Closer != nil {
		_ = record.Closer.Close()
	}
	delete(r.byObject, key)
	if r.byType[record.PluginType] > 0 {
		r.byType[record.PluginType]--
	}
}

func (r *RuntimeReloader) observeLocked() {
	for _, pluginType := range []kaprov1alpha1.PluginType{kaprov1alpha1.PluginTypeActuator, kaprov1alpha1.PluginTypeGate} {
		kaprometrics.PluginRuntimeRegistered.WithLabelValues(string(pluginType)).Set(float64(r.byType[pluginType]))
	}
}

func (r *RuntimeReloader) dial(ctx context.Context, c client.Reader, reg kaprov1alpha1.PluginRegistration) (*grpc.ClientConn, error) {
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

func isReadyForRuntime(reg kaprov1alpha1.PluginRegistration) bool {
	return reg.Status.Ready && reg.Status.ObservedGeneration == reg.Generation
}

func supportsRuntime(pluginType kaprov1alpha1.PluginType) bool {
	return pluginType == kaprov1alpha1.PluginTypeActuator || pluginType == kaprov1alpha1.PluginTypeGate
}

func objectKey(reg kaprov1alpha1.PluginRegistration) types.NamespacedName {
	return types.NamespacedName{Namespace: reg.Namespace, Name: reg.Name}
}

func registrationFingerprint(reg kaprov1alpha1.PluginRegistration) (string, error) {
	data, err := json.Marshal(reg.Spec)
	if err != nil {
		return "", fmt.Errorf("marshal plugin registration spec: %w", err)
	}
	return string(data), nil
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

func observeRuntimeCall(pluginType kaprov1alpha1.PluginType, name, method, result string, start time.Time) {
	labels := []string{string(pluginType), name, method, result}
	kaprometrics.PluginRuntimeCalls.WithLabelValues(labels...).Inc()
	kaprometrics.PluginRuntimeCallDuration.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
}
