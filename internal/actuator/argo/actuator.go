package argo

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

var applicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// Actuator implements KAI for Argo CD Applications.
type Actuator struct {
	Client client.Client
}

var _ actuator.Actuator = (*Actuator)(nil)
var _ actuator.BackendObjectReporter = (*Actuator)(nil)

func (a *Actuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	if req.Cluster == nil {
		return fmt.Errorf("cluster is nil")
	}
	appKey := req.AppKey
	if appKey == "" {
		appKey = "default"
	}
	return a.setTargetRevision(ctx, req.Cluster, appKey, req.Version)
}

func (a *Actuator) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("cluster is nil")
	}
	count := 0
	for appKey, version := range req.DesiredVersions {
		if version == "" {
			continue
		}
		if req.Cluster.Status.CurrentVersions[appKey] == version {
			continue
		}
		if err := a.setTargetRevision(ctx, req.Cluster, appKey, version); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (a *Actuator) IsConverged(ctx context.Context, mc *kaprov1alpha1.FleetCluster, appKey, version string) (bool, error) {
	if mc == nil {
		return false, fmt.Errorf("cluster is nil")
	}
	apps, err := a.getApplications(ctx, mc, appKey)
	if err != nil {
		return false, err
	}
	field := argoVersionField(mc, appKey)
	for i := range apps {
		app := &apps[i]
		if applicationTargetRevision(app, field) != version {
			return false, nil
		}
		syncStatus, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
		healthStatus, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
		if syncStatus != "Synced" || healthStatus != "Healthy" {
			return false, nil
		}
	}
	return true, nil
}

func (a *Actuator) IsAllConverged(ctx context.Context, mc *kaprov1alpha1.FleetCluster, desiredVersions map[string]string) (bool, error) {
	for appKey, version := range desiredVersions {
		ok, err := a.IsConverged(ctx, mc, appKey, version)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (a *Actuator) Rollback(ctx context.Context, mc *kaprov1alpha1.FleetCluster, previousVersion, appKey string) error {
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster: mc,
		Version: previousVersion,
		AppKey:  appKey,
	})
}

func (a *Actuator) setTargetRevision(ctx context.Context, mc *kaprov1alpha1.FleetCluster, appKey, version string) error {
	apps, err := a.getApplications(ctx, mc, appKey)
	if err != nil {
		return err
	}
	for i := range apps {
		app := &apps[i]
		if err := authorizeApplicationWrite(mc, app, appKey); err != nil {
			return err
		}
		patch := client.MergeFrom(app.DeepCopy())
		annotations := app.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["argocd.argoproj.io/refresh"] = "hard"
		app.SetAnnotations(annotations)
		if err := setApplicationTargetRevision(app, version, argoVersionField(mc, appKey)); err != nil {
			return fmt.Errorf("set Argo CD targetRevision: %w", err)
		}
		syncOperation := map[string]any{}
		if syncOptions, ok, _ := unstructured.NestedStringSlice(app.Object, "spec", "syncPolicy", "syncOptions"); ok {
			syncOperation["syncOptions"] = stringSliceToAny(syncOptions)
		}
		operation := map[string]any{
			"initiatedBy": map[string]any{
				"username":  "kapro-controller",
				"automated": true,
			},
			"info": []any{
				map[string]any{"name": "Reason", "value": "Kapro promotion requested a sync of this Application."},
				map[string]any{"name": "kapro.io/version", "value": version},
				map[string]any{"name": "kapro.io/unit", "value": appKey},
			},
			"sync": syncOperation,
		}
		if err := unstructured.SetNestedField(app.Object, operation, "operation"); err != nil {
			return fmt.Errorf("set Argo CD sync operation: %w", err)
		}
		if err := a.Client.Patch(ctx, app, patch); err != nil {
			return fmt.Errorf("patch Argo CD Application %s/%s: %w", app.GetNamespace(), app.GetName(), err)
		}
	}
	return nil
}

func stringSliceToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func (a *Actuator) getApplications(ctx context.Context, mc *kaprov1alpha1.FleetCluster, appKey string) ([]unstructured.Unstructured, error) {
	if a.Client == nil {
		return nil, fmt.Errorf("client is nil")
	}
	namespace := mc.Spec.Delivery.Param("namespace", "argocd")
	if selectorRaw := applicationSelector(mc, appKey); selectorRaw != "" {
		selector, err := labels.Parse(selectorRaw)
		if err != nil {
			return nil, fmt.Errorf("parse Argo CD application selector %q: %w", selectorRaw, err)
		}
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(applicationGVR.GroupVersion().WithKind("ApplicationList"))
		if err := a.Client.List(ctx, list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return nil, fmt.Errorf("list Argo CD Applications in %s: %w", namespace, err)
		}
		if len(list.Items) == 0 {
			return nil, fmt.Errorf("selector %q matched no Argo CD Applications in %s", selectorRaw, namespace)
		}
		sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].GetName() < list.Items[j].GetName() })
		return list.Items, nil
	}
	name := applicationName(mc, appKey)
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(applicationGVR.GroupVersion().WithKind("Application"))
	if err := a.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, app); err != nil {
		return nil, fmt.Errorf("get Argo CD Application %s/%s: %w", namespace, name, err)
	}
	return []unstructured.Unstructured{*app}, nil
}

func applicationName(mc *kaprov1alpha1.FleetCluster, appKey string) string {
	if appKey != "" && appKey != "default" {
		if name := mc.Spec.Delivery.Param("application."+appKey, ""); name != "" {
			return name
		}
	}
	if name := mc.Spec.Delivery.Param("application", ""); name != "" {
		return name
	}
	if appKey != "" && appKey != "default" {
		return mc.Name + "-" + appKey
	}
	return mc.Name
}

func applicationSelector(mc *kaprov1alpha1.FleetCluster, appKey string) string {
	if appKey != "" && appKey != "default" {
		if selector := mc.Spec.Delivery.Param("applicationSelector."+appKey, ""); selector != "" {
			return selector
		}
	}
	return mc.Spec.Delivery.Param("applicationSelector", "")
}

func argoVersionField(mc *kaprov1alpha1.FleetCluster, appKey string) string {
	if appKey != "" && appKey != "default" {
		if field := mc.Spec.Delivery.Param("versionField."+appKey, ""); field != "" {
			return field
		}
	}
	return mc.Spec.Delivery.Param("versionField", "spec.source.targetRevision")
}

func setApplicationTargetRevision(app *unstructured.Unstructured, version, field string) error {
	if field == "" || field == "spec.source.targetRevision" {
		return unstructured.SetNestedField(app.Object, version, "spec", "source", "targetRevision")
	}
	if index, ok := sourceIndexField(field); ok {
		sources, ok, err := unstructured.NestedSlice(app.Object, "spec", "sources")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("spec.sources is missing")
		}
		if index < 0 || index >= len(sources) {
			return fmt.Errorf("spec.sources[%d] is out of range", index)
		}
		source, ok := sources[index].(map[string]any)
		if !ok {
			return fmt.Errorf("spec.sources[%d] is not an object", index)
		}
		source["targetRevision"] = version
		sources[index] = source
		return unstructured.SetNestedSlice(app.Object, sources, "spec", "sources")
	}
	return fmt.Errorf("unsupported Argo Application version field %q", field)
}

func applicationTargetRevision(app *unstructured.Unstructured, field string) string {
	if field == "" || field == "spec.source.targetRevision" {
		value, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
		return value
	}
	if index, ok := sourceIndexField(field); ok {
		sources, ok, _ := unstructured.NestedSlice(app.Object, "spec", "sources")
		if !ok || index < 0 || index >= len(sources) {
			return ""
		}
		source, ok := sources[index].(map[string]any)
		if !ok {
			return ""
		}
		value, _ := source["targetRevision"].(string)
		return value
	}
	return ""
}

func sourceIndexField(field string) (int, bool) {
	var index int
	if _, err := fmt.Sscanf(field, "spec.sources[%d].targetRevision", &index); err == nil {
		return index, true
	}
	return 0, false
}

func authorizeApplicationWrite(mc *kaprov1alpha1.FleetCluster, app *unstructured.Unstructured, appKey string) error {
	if mc.Spec.Delivery.Param("authorization", "required") == "disabled" ||
		mc.Spec.Delivery.Param("requireAuthorization", "true") == "false" {
		return nil
	}
	authorizedSource := mc.Spec.Delivery.Param("authorizedSource", "")
	values := []string{
		app.GetLabels()["kapro.io/managed-by"],
		app.GetAnnotations()["kapro.io/managed-by"],
	}
	for _, value := range values {
		if value == "kapro" {
			return nil
		}
	}
	sourceValues := []string{
		app.GetLabels()["kapro.io/authorized-source"],
		app.GetAnnotations()["kapro.io/authorized-source"],
	}
	for _, value := range sourceValues {
		if value == "" {
			continue
		}
		if value == "*" || authorizedSource == "" || value == authorizedSource {
			return nil
		}
	}
	unitValues := []string{
		app.GetLabels()["kapro.io/authorized-unit"],
		app.GetAnnotations()["kapro.io/authorized-unit"],
	}
	for _, value := range unitValues {
		if value == "*" || value == appKey {
			return nil
		}
	}
	return fmt.Errorf("argo CD Application %s/%s is not authorized for Kapro adoption; set kapro.io/managed-by=kapro or kapro.io/authorized-source",
		app.GetNamespace(), app.GetName())
}

func (a *Actuator) BackendObjects(ctx context.Context, mc *kaprov1alpha1.FleetCluster, desiredVersions map[string]string) ([]kaprov1alpha1.BackendObjectStatus, error) {
	var statuses []kaprov1alpha1.BackendObjectStatus
	for appKey, version := range desiredVersions {
		apps, err := a.getApplications(ctx, mc, appKey)
		if err != nil {
			return nil, err
		}
		field := argoVersionField(mc, appKey)
		for i := range apps {
			app := &apps[i]
			current := applicationTargetRevision(app, field)
			syncStatus, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
			healthStatus, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
			phase := "Progressing"
			message := "waiting for Argo CD Application to match desired revision and become Synced/Healthy"
			if current == version && syncStatus == "Synced" && healthStatus == "Healthy" {
				phase = "Converged"
				message = "Argo CD Application is Synced and Healthy at desired revision"
			}
			statuses = append(statuses, kaprov1alpha1.BackendObjectStatus{
				APIVersion:     "argoproj.io/v1alpha1",
				Kind:           "Application",
				Namespace:      app.GetNamespace(),
				Name:           app.GetName(),
				Unit:           appKey,
				DesiredVersion: version,
				CurrentVersion: current,
				SyncStatus:     syncStatus,
				HealthStatus:   healthStatus,
				Phase:          phase,
				Message:        message,
			})
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Unit == statuses[j].Unit {
			return statuses[i].Name < statuses[j].Name
		}
		return statuses[i].Unit < statuses[j].Unit
	})
	return statuses, nil
}
