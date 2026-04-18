// Package kserve implements an Actuator for KServe InferenceService resources.
// It promotes AI/ML model versions by patching spec.predictor.model.storageUri.
// Uses dynamic client — no KServe type imports to avoid version conflicts.
package kserve

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

// Compile-time assertion: Actuator must satisfy the actuator.Actuator interface.
var _ actuator.Actuator = (*Actuator)(nil)

var inferenceServiceGVR = schema.GroupVersionResource{
	Group:    "serving.kserve.io",
	Version:  "v1beta1",
	Resource: "inferenceservices",
}

// Actuator promotes AI model versions by patching KServe InferenceService resources.
//
// For each target environment, it:
//  1. Resolves the storage URI from the version string or the configured template.
//  2. Patches spec.predictor.model.storageUri on the InferenceService.
//  3. Polls status.conditions for Ready=True to detect convergence.
type Actuator struct {
	// Config is the REST config for the target cluster.
	Config *rest.Config
}

// Apply patches the InferenceService's storageUri to roll out the new model version.
func (a *Actuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	logger := log.FromContext(ctx).WithValues(
		"environment", req.Environment.Name,
		"version", req.Version,
	)

	kserveSpec := a.kserveSpec(req.Environment)
	if kserveSpec == nil {
		return fmt.Errorf("KServeActuator.Apply: environment %s has no kserve actuator spec", req.Environment.Name)
	}

	dynClient, err := dynamic.NewForConfig(a.Config)
	if err != nil {
		return fmt.Errorf("KServeActuator.Apply: create dynamic client: %w", err)
	}

	ns := kserveSpec.Namespace
	if ns == "" {
		ns = "default"
	}

	storageURI := resolveStorageURI(kserveSpec.StorageURITemplate, req.Version)

	patch := fmt.Sprintf(`{"spec":{"predictor":{"model":{"storageUri":%q}}}}`, storageURI)
	_, err = dynClient.Resource(inferenceServiceGVR).Namespace(ns).Patch(
		ctx,
		kserveSpec.InferenceServiceName,
		types.MergePatchType,
		[]byte(patch),
		*metav1PatchOptions(),
	)
	if err != nil {
		return fmt.Errorf("KServeActuator.Apply: patch InferenceService %s/%s: %w", ns, kserveSpec.InferenceServiceName, err)
	}

	logger.Info("patched InferenceService storageUri",
		"namespace", ns,
		"inferenceService", kserveSpec.InferenceServiceName,
		"storageUri", storageURI,
	)
	return nil
}

// IsConverged returns true when the InferenceService Ready condition is True.
func (a *Actuator) IsConverged(ctx context.Context, env *kaprov1alpha1.Environment, version string) (bool, error) {
	kserveSpec := a.kserveSpec(env)
	if kserveSpec == nil {
		return false, fmt.Errorf("KServeActuator.IsConverged: environment %s has no kserve actuator spec", env.Name)
	}

	dynClient, err := dynamic.NewForConfig(a.Config)
	if err != nil {
		return false, fmt.Errorf("KServeActuator.IsConverged: create dynamic client: %w", err)
	}

	ns := kserveSpec.Namespace
	if ns == "" {
		ns = "default"
	}

	obj, err := dynClient.Resource(inferenceServiceGVR).Namespace(ns).Get(
		ctx,
		kserveSpec.InferenceServiceName,
		*metav1GetOptions(),
	)
	if err != nil {
		return false, fmt.Errorf("KServeActuator.IsConverged: get InferenceService %s/%s: %w", ns, kserveSpec.InferenceServiceName, err)
	}

	// Verify the storage URI reflects the desired version.
	currentURI, _, _ := unstructured.NestedString(obj.Object, "spec", "predictor", "model", "storageUri")
	wantURI := resolveStorageURI(kserveSpec.StorageURITemplate, version)
	if currentURI != wantURI {
		log.FromContext(ctx).Info("storageUri not yet updated",
			"current", currentURI, "want", wantURI)
		return false, nil
	}

	// Check Ready condition.
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" && cond["status"] == "True" {
			return true, nil
		}
	}
	return false, nil
}

// Rollback patches the InferenceService back to the previous version.
func (a *Actuator) Rollback(ctx context.Context, env *kaprov1alpha1.Environment, previousVersion string) error {
	log.FromContext(ctx).Info("rolling back InferenceService",
		"environment", env.Name,
		"previousVersion", previousVersion,
	)
	return a.Apply(ctx, actuator.ApplyRequest{
		Environment: env,
		Version:     previousVersion,
	})
}

// kserveSpec extracts the KServe actuator spec from an Environment.
func (a *Actuator) kserveSpec(env *kaprov1alpha1.Environment) *kaprov1alpha1.KServeActuatorSpec {
	if env.Spec.Actuator.KServe == nil {
		return nil
	}
	return env.Spec.Actuator.KServe
}

// resolveStorageURI returns a storage URI for the given version.
// If the template is empty, the version is returned as-is (it may already be a full URI).
// Simple template: replaces "{{.Version}}" with the actual version string.
func resolveStorageURI(template, version string) string {
	if template == "" {
		return version
	}
	return strings.ReplaceAll(template, "{{.Version}}", version)
}

func metav1PatchOptions() *metav1.PatchOptions {
	return &metav1.PatchOptions{}
}

func metav1GetOptions() *metav1.GetOptions {
	return &metav1.GetOptions{}
}
