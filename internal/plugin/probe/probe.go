// Package probe validates PluginRegistration endpoints and reads capabilities.
package probe

import (
	"context"
	"fmt"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/plugin/transport"
	"kapro.io/kapro/pkg/plugincompat"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var plannerCapabilities = map[string]struct{}{
	"filter": {},
	"score":  {},
	"order":  {},
	"defer":  {},
}

// Result is the normalized outcome of a plugin probe.
type Result struct {
	Ready           bool
	Reason          string
	Message         string
	Version         string
	ContractVersion string
	Capabilities    []string
}

// Prober probes registered plugin endpoints without executing promotionrun traffic.
type Prober struct {
	Client      client.Reader
	DialOptions []grpc.DialOption
}

// Probe validates the registration, dials the endpoint, and reads capabilities.
func (p Prober) Probe(ctx context.Context, reg kaprov1alpha1.Plugin) (result Result) {
	start := time.Now()
	defer func() { observeProbe(reg, result, start) }()

	if reg.Spec.Type != kaprov1alpha1.PluginTypeActuator &&
		reg.Spec.Type != kaprov1alpha1.PluginTypeGate &&
		reg.Spec.Type != kaprov1alpha1.PluginTypePlanner {
		return notReady("UnsupportedType", fmt.Sprintf("plugin registration %q has unsupported type %q", registrationName(reg), reg.Spec.Type))
	}
	if reg.Spec.Protocol != "" && reg.Spec.Protocol != kaprov1alpha1.PluginProtocolGRPC {
		return notReady("UnsupportedProtocol", fmt.Sprintf("plugin %q uses unsupported protocol %q", registrationName(reg), reg.Spec.Protocol))
	}
	if reg.Spec.Endpoint == "" {
		return notReady("InvalidEndpoint", fmt.Sprintf("plugin %q endpoint is required", registrationName(reg)))
	}
	timeout := 10 * time.Second
	if reg.Spec.Timeout != "" {
		parsed, err := time.ParseDuration(reg.Spec.Timeout)
		if err != nil {
			return notReady("InvalidTimeout", fmt.Sprintf("plugin %q has invalid timeout %q: %v", registrationName(reg), reg.Spec.Timeout, err))
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts, err := transport.DialOptions(ctx, p.Client, reg)
	if err != nil {
		return notReady("TLSInvalid", fmt.Sprintf("plugin %q TLS configuration for endpoint %q is invalid: %v", registrationName(reg), reg.Spec.Endpoint, err))
	}
	opts = append(opts, grpc.WithBlock()) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	opts = append(opts, p.DialOptions...)
	conn, err := grpc.DialContext(ctx, reg.Spec.Endpoint, opts...) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	if err != nil {
		return notReady("DialFailed", fmt.Sprintf("plugin %q dial to endpoint %q failed within %s: %v", registrationName(reg), reg.Spec.Endpoint, timeout, err))
	}
	defer conn.Close()

	switch reg.Spec.Type {
	case kaprov1alpha1.PluginTypeActuator:
		resp, err := kaiv1alpha1.NewActuatorServiceClient(conn).GetCapabilities(ctx, &kaiv1alpha1.GetCapabilitiesRequest{})
		if err != nil {
			return notReady("ProbeFailed", fmt.Sprintf("actuator plugin %q GetCapabilities failed: %v", registrationName(reg), err))
		}
		if result := validateContract(reg.Spec.Type, resp.GetContractVersion()); !result.Ready {
			return result
		}
		return ready(resp.GetPluginVersion(), resp.GetContractVersion(), resp.GetCapabilities())
	case kaprov1alpha1.PluginTypeGate:
		resp, err := kgiv1alpha1.NewGateServiceClient(conn).GetCapabilities(ctx, &kgiv1alpha1.GetCapabilitiesRequest{})
		if err != nil {
			return notReady("ProbeFailed", fmt.Sprintf("gate plugin %q GetCapabilities failed: %v", registrationName(reg), err))
		}
		if result := validateContract(reg.Spec.Type, resp.GetContractVersion()); !result.Ready {
			return result
		}
		return ready(resp.GetPluginVersion(), resp.GetContractVersion(), resp.GetCapabilities())
	case kaprov1alpha1.PluginTypePlanner:
		resp, err := kpiv1alpha1.NewPlannerServiceClient(conn).GetCapabilities(ctx, &kpiv1alpha1.GetCapabilitiesRequest{})
		if err != nil {
			return notReady("ProbeFailed", fmt.Sprintf("planner plugin %q GetCapabilities failed: %v", registrationName(reg), err))
		}
		if result := validateContract(reg.Spec.Type, resp.GetContractVersion()); !result.Ready {
			return result
		}
		if !hasPlannerCapability(resp.GetCapabilities()) {
			return notReadyWithContract("MissingCapability", "planner plugin must report at least one capability: filter, score, order, or defer", resp.GetContractVersion())
		}
		return ready(resp.GetPluginVersion(), resp.GetContractVersion(), resp.GetCapabilities())
	default:
		return notReady("UnsupportedType", fmt.Sprintf("plugin registration %q has unsupported type %q", registrationName(reg), reg.Spec.Type))
	}
}

func validateContract(pluginType kaprov1alpha1.PluginType, version string) Result {
	supported := plugincompat.SupportedContractVersionsString(pluginType)
	contractName := plugincompat.ContractName(pluginType)
	if version == "" {
		return notReadyWithContract(
			"MissingContractVersion",
			fmt.Sprintf("plugin did not report %s contract version; supported versions: %s", contractName, supported),
			version,
		)
	}
	if !plugincompat.IsContractVersionSupported(pluginType, version) {
		return notReadyWithContract(
			"UnsupportedContractVersion",
			fmt.Sprintf("plugin reported unsupported %s contract version %q; supported versions: %s", contractName, version, supported),
			version,
		)
	}
	return Result{Ready: true, ContractVersion: version}
}

func hasPlannerCapability(capabilities []string) bool {
	for _, capability := range capabilities {
		if _, ok := plannerCapabilities[capability]; ok {
			return true
		}
	}
	return false
}

func ready(version, contractVersion string, capabilities []string) Result {
	return Result{
		Ready:           true,
		Reason:          "ProbeSucceeded",
		Message:         "plugin capabilities probe succeeded",
		Version:         version,
		ContractVersion: contractVersion,
		Capabilities:    append([]string(nil), capabilities...),
	}
}

func notReady(reason, message string) Result {
	return Result{
		Ready:   false,
		Reason:  reason,
		Message: message,
	}
}

func notReadyWithContract(reason, message, contractVersion string) Result {
	result := notReady(reason, message)
	result.ContractVersion = contractVersion
	return result
}

// ContractVersion returns the KAI/KGI/KPI contract version this prober supports.
func ContractVersion() string {
	return plugincompat.VersionV1Alpha1
}

func observeProbe(reg kaprov1alpha1.Plugin, result Result, start time.Time) {
	outcome := "error"
	readyValue := 0.0
	if result.Ready {
		outcome = "success"
		readyValue = 1
	}
	reason := result.Reason
	if reason == "" {
		reason = "Unknown"
	}
	pluginType := string(reg.Spec.Type)
	if pluginType == "" {
		pluginType = "unknown"
	}
	kaprometrics.PluginProbeResults.WithLabelValues(pluginType, outcome, reason).Inc()
	kaprometrics.PluginProbeDuration.WithLabelValues(pluginType, outcome).Observe(time.Since(start).Seconds())
	kaprometrics.PluginProbeReady.WithLabelValues(pluginType, registrationMetricName(reg)).Set(readyValue)
	if reg.Name != "" && reg.Name != reg.Spec.Name {
		kaprometrics.PluginProbeReady.DeleteLabelValues(pluginType, reg.Name)
	}
}

func registrationName(reg kaprov1alpha1.Plugin) string {
	if reg.Name != "" {
		return reg.Name
	}
	if reg.Spec.Name != "" {
		return reg.Spec.Name
	}
	return "<unnamed>"
}

func registrationMetricName(reg kaprov1alpha1.Plugin) string {
	if reg.Spec.Name != "" {
		return reg.Spec.Name
	}
	return registrationName(reg)
}

// ForgetReadiness removes the per-registration readiness metric for a deleted
// registration. Callers should pass the last observed object before finalizer
// removal so the registry key is still available.
func ForgetReadiness(reg kaprov1alpha1.Plugin) {
	pluginType := string(reg.Spec.Type)
	if pluginType == "" {
		pluginType = "unknown"
	}
	kaprometrics.PluginProbeReady.WithLabelValues(pluginType, registrationMetricName(reg)).Set(0)
	kaprometrics.PluginProbeReady.DeleteLabelValues(pluginType, registrationMetricName(reg))
	if reg.Name != "" && reg.Name != reg.Spec.Name {
		kaprometrics.PluginProbeReady.WithLabelValues(pluginType, reg.Name).Set(0)
		kaprometrics.PluginProbeReady.DeleteLabelValues(pluginType, reg.Name)
	}
}
