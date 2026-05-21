package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	pluginadapter "kapro.io/kapro/internal/plugin/adapter"
	"kapro.io/kapro/internal/plugin/probe"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/planner"
	"kapro.io/kapro/pkg/plugincompat"
)

// pluginSchemaConditionType marks a PluginRegistration whose plugin-reported
// schema (contract version or capability set) has drifted from the previously
// stored snapshot. It is informational: the runtime continues to use the new
// schema, but the condition + event give operators a chance to catch breaking
// upgrades of an external plugin image.
const pluginSchemaConditionType = "SchemaChanged"

// pluginSchemaHash returns a deterministic sha256 of (contractVersion + sorted
// capabilities). The format is stable across re-orderings so hot-reloads that
// merely re-shuffle the capability list don't appear as drift.
func pluginSchemaHash(contractVersion string, capabilities []string) string {
	sorted := append([]string(nil), capabilities...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(contractVersion + "|" + strings.Join(sorted, ",")))
	return hex.EncodeToString(sum[:])
}

// PluginReconciler probes external plugin registrations and records
// readiness status. When the plugin gateway is enabled, it also hot-loads ready
// runtime adapters and unloads adapters whose registration is no longer ready.
type PluginReconciler struct {
	client.Client
	Recorder         record.EventRecorder
	Prober           PluginProber
	RuntimeEnabled   bool
	RuntimeRegistrar pluginadapter.Registrar
	ActuatorRegistry *actuator.Registry
	GateRegistry     *gate.Registry
	Planner          *planner.Framework
}

const pluginRegistrationMetricsFinalizer = "kapro.io/plugin-registration-metrics"

// PluginProber is the dependency used to probe plugin endpoints.
type PluginProber interface {
	Probe(ctx context.Context, reg kaprov1alpha2.Plugin) probe.Result
}

// +kubebuilder:rbac:groups=kapro.io,resources=plugins,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=plugins,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=plugins/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *PluginReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var reg kaprov1alpha2.Plugin
	if err := r.Get(ctx, req.NamespacedName, &reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !reg.DeletionTimestamp.IsZero() {
		probe.ForgetReadiness(reg)
		if r.RuntimeEnabled {
			r.RuntimeRegistrar.UnregisterOne(reg, r.ActuatorRegistry, r.GateRegistry, r.Planner)
		}
		if controllerutil.RemoveFinalizer(&reg, pluginRegistrationMetricsFinalizer) {
			if err := r.Update(ctx, &reg); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove plugin registration metrics finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&reg, pluginRegistrationMetricsFinalizer) {
		if err := r.Update(ctx, &reg); err != nil {
			return ctrl.Result{}, fmt.Errorf("add plugin registration metrics finalizer: %w", err)
		}
	}

	prober := r.Prober
	if prober == nil {
		prober = probe.Prober{Client: r.Client}
	}
	result := prober.Probe(ctx, reg)
	wasRuntimeReady := isRuntimeReady(reg)
	previousSchemaHash := reg.Status.SchemaHash
	previousContractVersion := reg.Status.ContractVersion
	previousCapabilities := append([]string(nil), reg.Status.Capabilities...)
	patch := client.MergeFrom(reg.DeepCopy())
	now := metav1.Now()
	reg.Status.ObservedGeneration = reg.Generation
	reg.Status.Ready = result.Ready
	reg.Status.Version = result.Version
	reg.Status.ContractVersion = result.ContractVersion
	reg.Status.Capabilities = result.Capabilities
	if result.Ready {
		reg.Status.LastSeen = now.UTC().Format(time.RFC3339)
	}
	// Update schema snapshot only when the probe reported a usable schema; do
	// not clobber the previously-stored hash on a transient probe failure.
	newSchemaHash := previousSchemaHash
	schemaChanged := false
	if result.Ready && result.ContractVersion != "" {
		newSchemaHash = pluginSchemaHash(result.ContractVersion, result.Capabilities)
		reg.Status.SchemaHash = newSchemaHash
		if previousSchemaHash != "" && previousSchemaHash != newSchemaHash {
			schemaChanged = true
		}
	}

	status := metav1.ConditionFalse
	if result.Ready {
		status = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             result.Reason,
		Message:            result.Message,
		ObservedGeneration: reg.Generation,
		LastTransitionTime: now,
	})
	apimeta.SetStatusCondition(&reg.Status.Conditions, compatibleCondition(reg.Spec.Type, result, reg.Generation, now))
	apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeReconciling,
		Status:             metav1.ConditionFalse,
		Reason:             result.Reason,
		Message:            "plugin registration probe completed",
		ObservedGeneration: reg.Generation,
		LastTransitionTime: now,
	})
	if result.Ready {
		apimeta.RemoveStatusCondition(&reg.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	} else {
		apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
			Type:               kaprov1alpha2.ConditionTypeStalled,
			Status:             metav1.ConditionTrue,
			Reason:             result.Reason,
			Message:            result.Message,
			ObservedGeneration: reg.Generation,
			LastTransitionTime: now,
		})
	}
	if schemaChanged {
		apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
			Type:               pluginSchemaConditionType,
			Status:             metav1.ConditionTrue,
			Reason:             "PluginSchemaDrift",
			Message:            describePluginSchemaDrift(previousContractVersion, previousCapabilities, result.ContractVersion, result.Capabilities),
			ObservedGeneration: reg.Generation,
			LastTransitionTime: now,
		})
	} else if previousSchemaHash != "" && newSchemaHash == previousSchemaHash {
		// Stable schema — drop a stale SchemaChanged condition so operators
		// see it clear after they have acknowledged the drift.
		apimeta.RemoveStatusCondition(&reg.Status.Conditions, pluginSchemaConditionType)
	}

	if err := r.Status().Patch(ctx, &reg, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch plugin registration status: %w", err)
	}
	if schemaChanged && r.Recorder != nil {
		r.Recorder.Eventf(&reg, corev1.EventTypeWarning, "PluginSchemaDrift",
			"plugin %q schema changed between hot-reloads: %s", reg.Name,
			describePluginSchemaDrift(previousContractVersion, previousCapabilities, result.ContractVersion, result.Capabilities))
	}
	if r.RuntimeEnabled {
		if isRuntimeReady(reg) {
			if err := r.RuntimeRegistrar.RegisterOne(ctx, r.Client, reg, r.ActuatorRegistry, r.GateRegistry, r.Planner); err != nil {
				return ctrl.Result{}, fmt.Errorf("register runtime plugin %q: %w", reg.Name, err)
			}
		} else if wasRuntimeReady {
			r.RuntimeRegistrar.UnregisterOne(reg, r.ActuatorRegistry, r.GateRegistry, r.Planner)
		}
	}

	eventType := corev1.EventTypeWarning
	if result.Ready {
		eventType = corev1.EventTypeNormal
	}
	r.Recorder.Event(&reg, eventType, result.Reason, result.Message)
	log.Info("plugin registration probed", "name", reg.Name, "type", reg.Spec.Type, "ready", result.Ready, "reason", result.Reason)

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func isRuntimeReady(reg kaprov1alpha2.Plugin) bool {
	return reg.Status.Ready && reg.Status.ObservedGeneration == reg.Generation
}

func compatibleCondition(pluginType kaprov1alpha2.PluginType, result probe.Result, observedGeneration int64, now metav1.Time) metav1.Condition {
	condition := metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeCompatible,
		Status:             metav1.ConditionUnknown,
		Reason:             result.Reason,
		Message:            "plugin contract compatibility could not be determined",
		ObservedGeneration: observedGeneration,
		LastTransitionTime: now,
	}
	switch result.Reason {
	case "ProbeSucceeded":
		condition.Status = metav1.ConditionTrue
		condition.Message = fmt.Sprintf("plugin contract version %q is supported", result.ContractVersion)
	case "MissingContractVersion", "UnsupportedContractVersion":
		condition.Status = metav1.ConditionFalse
		condition.Message = result.Message
	default:
		if result.ContractVersion != "" && plugincompat.IsContractVersionSupported(pluginType, result.ContractVersion) {
			condition.Status = metav1.ConditionTrue
			condition.Message = fmt.Sprintf("plugin contract version %q is supported", result.ContractVersion)
			return condition
		}
		if result.Message != "" {
			condition.Message = fmt.Sprintf("plugin contract compatibility could not be determined: %s", result.Message)
		}
	}
	return condition
}

func (r *PluginReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.Plugin{}).
		Complete(r)
}

// describePluginSchemaDrift returns a human-readable diff of the changed
// contract version and added/removed capabilities. Used for status messages
// and Warning events so operators see *what* changed, not just *that* it did.
func describePluginSchemaDrift(oldContract string, oldCaps []string, newContract string, newCaps []string) string {
	var parts []string
	if oldContract != newContract {
		parts = append(parts, fmt.Sprintf("contractVersion %q → %q", oldContract, newContract))
	}
	oldSet := make(map[string]struct{}, len(oldCaps))
	for _, c := range oldCaps {
		oldSet[c] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newCaps))
	for _, c := range newCaps {
		newSet[c] = struct{}{}
	}
	var added, removed []string
	for c := range newSet {
		if _, ok := oldSet[c]; !ok {
			added = append(added, c)
		}
	}
	for c := range oldSet {
		if _, ok := newSet[c]; !ok {
			removed = append(removed, c)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	if len(added) > 0 {
		parts = append(parts, "added capabilities: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed capabilities: "+strings.Join(removed, ", "))
	}
	if len(parts) == 0 {
		return "schema hash changed but no per-field diff detected"
	}
	return strings.Join(parts, "; ")
}
