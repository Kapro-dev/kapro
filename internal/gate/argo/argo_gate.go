// Package argo implements the Argo Rollouts AnalysisRun gate.
//
// Kapro creates an AnalysisRun in the Argo namespace, reads its phase,
// and translates the result to a GateResult. Kapro does not know about
// Argo's internal metric evaluation logic — it only reads the terminal phase.
//
// This is the "containerd" of Kapro's gate system: Kapro owns the lifecycle
// (when to start, timeout, cleanup), Argo owns the execution.
//
// Idempotency: AnalysisRun names are deterministic ({promotionName}-{templateName}).
// On every reconcile, the gate does CreateOrGet — never duplicates.
package argo

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

// AnalysisRun Argo GVR — we use unstructured to avoid importing argo-rollouts types.
var analysisRunGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "analysisruns",
}

var analysisRunGVK = schema.GroupVersionKind{
	Group:   "argoproj.io",
	Version: "v1alpha1",
	Kind:    "AnalysisRun",
}

// Argo AnalysisRun phase constants — mirrored from argoproj/argo-rollouts
// without importing the full package.
const (
	analysisPhaseRunning      = "Running"
	analysisPhaseSuccessful   = "Successful"
	analysisPhaseFailed       = "Failed"
	analysisPhaseError        = "Error"
	analysisPhaseInconclusive = "Inconclusive"
	analysisPhasePending      = "Pending"
)

// Gate creates an Argo AnalysisRun and translates its phase to a GateResult.
// Uses controller-runtime's dynamic client (unstructured) to avoid hard dep
// on argo-rollouts Go module.
type Gate struct {
	Client client.Client
}

// Evaluate ensures an AnalysisRun exists for this promotion+template pair,
// reads its current phase, and returns the normalised GateResult.
// The AnalysisRun name is deterministic — safe to call on every reconcile.
func (g *Gate) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	log := log.FromContext(ctx)

	if req.Template == nil || req.Template.Spec.ArgoAnalysis == nil {
		return pkggate.Result{}, fmt.Errorf("argo gate: template or argoAnalysis spec is nil")
	}

	spec := req.Template.Spec.ArgoAnalysis
	ns := spec.Namespace
	if ns == "" {
		ns = "argo-rollouts"
	}

	runName := analysisRunName(req.Promotion.Name, req.Template.Name)
	log.Info("argo gate: ensuring AnalysisRun", "name", runName, "namespace", ns)

	// Ensure AnalysisRun exists (idempotent create).
	if err := g.ensureAnalysisRun(ctx, runName, ns, spec, req); err != nil {
		return pkggate.Result{}, fmt.Errorf("argo gate: ensure AnalysisRun: %w", err)
	}

	// Read current phase.
	run, err := g.getAnalysisRun(ctx, runName, ns)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("argo gate: get AnalysisRun: %w", err)
	}

	phase, message, conditions := extractPhase(run)
	log.Info("argo gate: AnalysisRun status", "phase", phase, "message", message)

	vendorRef := &corev1.ObjectReference{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "AnalysisRun",
		Name:       runName,
		Namespace:  ns,
	}

	switch phase {
	case analysisPhaseSuccessful:
		return pkggate.Result{
			Passed:    true,
			Phase:     kaprov1alpha1.GatePhasePassed,
			Message:   message,
			VendorRef: vendorRef,
			Results:   conditions,
		}, nil

	case analysisPhaseFailed, analysisPhaseError:
		return pkggate.Result{
			Passed:    false,
			Phase:     kaprov1alpha1.GatePhaseFailed,
			Message:   message,
			VendorRef: vendorRef,
			Results:   conditions,
		}, nil

	case analysisPhaseInconclusive:
		return pkggate.Result{
			Passed:     false,
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    message,
			RetryAfter: "30s",
			VendorRef:  vendorRef,
			Results:    conditions,
		}, nil

	default: // Running, Pending, or unknown
		return pkggate.Result{
			Passed:     false,
			Phase:      kaprov1alpha1.GatePhaseRunning,
			Message:    fmt.Sprintf("AnalysisRun %s/%s is %s", ns, runName, phase),
			RetryAfter: "30s",
			VendorRef:  vendorRef,
			Results:    conditions,
		}, nil
	}
}

// ensureAnalysisRun creates the AnalysisRun if it does not already exist.
func (g *Gate) ensureAnalysisRun(ctx context.Context, name, ns string, spec *kaprov1alpha1.ArgoAnalysisGateSpec, req pkggate.Request) error {
	run := &unstructured.Unstructured{}
	run.SetGroupVersionKind(analysisRunGVK)
	run.SetName(name)
	run.SetNamespace(ns)
	run.SetLabels(map[string]string{
		"kapro.io/promotion":     req.Promotion.Name,
		"kapro.io/gate-template": req.Template.Name,
	})

	// Wire args as AnalysisRun arguments.
	argoArgs := make([]any, 0, len(req.Args))
	for k, v := range req.Args {
		argoArgs = append(argoArgs, map[string]any{"name": k, "value": v})
	}

	if err := unstructured.SetNestedField(run.Object, map[string]any{
		"templates": []any{
			map[string]any{
				"templateName": spec.TemplateName,
			},
		},
		"args": argoArgs,
	}, "spec"); err != nil {
		return fmt.Errorf("set spec: %w", err)
	}

	if err := g.Client.Create(ctx, run); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil // already running — idempotent
		}
		return fmt.Errorf("create AnalysisRun: %w", err)
	}
	return nil
}

func (g *Gate) getAnalysisRun(ctx context.Context, name, ns string) (*unstructured.Unstructured, error) {
	run := &unstructured.Unstructured{}
	run.SetGroupVersionKind(analysisRunGVK)
	if err := g.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, run); err != nil {
		return nil, err
	}
	return run, nil
}

// analysisRunName returns a deterministic AnalysisRun name.
// Safe to call on every reconcile — idempotent via AlreadyExists check.
func analysisRunName(promotionName, templateName string) string {
	// Truncate to stay within 253-char Kubernetes name limit.
	name := fmt.Sprintf("%s-%s", promotionName, templateName)
	if len(name) > 253 {
		name = name[:253]
	}
	return strings.ToLower(name)
}

// extractPhase reads the phase + metric results from the unstructured AnalysisRun.
func extractPhase(run *unstructured.Unstructured) (phase, message string, results []pkggate.ConditionResult) {
	status, _, _ := unstructured.NestedMap(run.Object, "status")
	if status == nil {
		return analysisPhaseRunning, "AnalysisRun status not yet available", nil
	}

	phase, _, _ = unstructured.NestedString(run.Object, "status", "phase")
	message, _, _ = unstructured.NestedString(run.Object, "status", "message")

	if phase == "" {
		phase = analysisPhaseRunning
	}

	// Extract per-metric results.
	metricResults, _, _ := unstructured.NestedSlice(run.Object, "status", "metricResults")
	for _, r := range metricResults {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		mPhase, _ := m["phase"].(string)
		msg, _ := m["message"].(string)
		// Last measurement value.
		value := ""
		if measurements, ok := m["measurements"].([]any); ok && len(measurements) > 0 {
			if last, ok := measurements[len(measurements)-1].(map[string]any); ok {
				value, _ = last["value"].(string)
			}
		}
		results = append(results, pkggate.ConditionResult{
			Name:    name,
			Phase:   translateArgoPhase(mPhase),
			Value:   value,
			Message: msg,
		})
	}

	return phase, message, results
}

func translateArgoPhase(p string) kaprov1alpha1.GatePhase {
	switch p {
	case analysisPhaseSuccessful:
		return kaprov1alpha1.GatePhasePassed
	case analysisPhaseFailed, analysisPhaseError:
		return kaprov1alpha1.GatePhaseFailed
	case analysisPhaseInconclusive:
		return kaprov1alpha1.GatePhaseInconclusive
	case analysisPhasePending:
		return kaprov1alpha1.GatePhasePending
	default:
		return kaprov1alpha1.GatePhaseRunning
	}
}

// Ensure Gate implements pkggate.Gate at compile time.
var _ pkggate.Gate = (*Gate)(nil)

// keep metav1 used for the zero-value reference check below.
var _ = metav1.Now
var _ = time.Now
