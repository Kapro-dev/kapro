package argo

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func (a *Actuator) IsConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, appKey, version string) (bool, error) {
	if mc == nil {
		return false, fmt.Errorf("cluster is nil")
	}
	app, err := a.getApplication(ctx, mc, appKey)
	if err != nil {
		return false, err
	}
	targetRevision, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	if targetRevision != version {
		return false, nil
	}
	syncStatus, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	healthStatus, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	return syncStatus == "Synced" && healthStatus == "Healthy", nil
}

func (a *Actuator) IsAllConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, desiredVersions map[string]string) (bool, error) {
	for appKey, version := range desiredVersions {
		ok, err := a.IsConverged(ctx, mc, appKey, version)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (a *Actuator) Rollback(ctx context.Context, mc *kaprov1alpha1.MemberCluster, previousVersion, appKey string) error {
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster: mc,
		Version: previousVersion,
		AppKey:  appKey,
	})
}

func (a *Actuator) setTargetRevision(ctx context.Context, mc *kaprov1alpha1.MemberCluster, appKey, version string) error {
	app, err := a.getApplication(ctx, mc, appKey)
	if err != nil {
		return err
	}
	patch := client.MergeFrom(app.DeepCopy())
	annotations := app.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["argocd.argoproj.io/refresh"] = "hard"
	app.SetAnnotations(annotations)
	if err := unstructured.SetNestedField(app.Object, version, "spec", "source", "targetRevision"); err != nil {
		return fmt.Errorf("set Argo CD targetRevision: %w", err)
	}
	operation := map[string]any{
		"initiatedBy": map[string]any{
			"username":  "kapro-controller",
			"automated": true,
		},
		"info": []any{
			map[string]any{"name": "Reason", "value": "Kapro promotion requested a sync of this Application."},
			map[string]any{"name": "kapro.io/version", "value": version},
		},
		"sync": map[string]any{},
	}
	if syncOptions, ok, _ := unstructured.NestedStringSlice(app.Object, "spec", "syncPolicy", "syncOptions"); ok {
		operation["sync"].(map[string]any)["syncOptions"] = syncOptions
	}
	if err := unstructured.SetNestedField(app.Object, operation, "operation"); err != nil {
		return fmt.Errorf("set Argo CD sync operation: %w", err)
	}
	if err := a.Client.Patch(ctx, app, patch); err != nil {
		return fmt.Errorf("patch Argo CD Application %s/%s: %w", app.GetNamespace(), app.GetName(), err)
	}
	return nil
}

func (a *Actuator) getApplication(ctx context.Context, mc *kaprov1alpha1.MemberCluster, appKey string) (*unstructured.Unstructured, error) {
	if a.Client == nil {
		return nil, fmt.Errorf("client is nil")
	}
	name := applicationName(mc, appKey)
	namespace := mc.Spec.Delivery.Param("namespace", "argocd")
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(applicationGVR.GroupVersion().WithKind("Application"))
	if err := a.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, app); err != nil {
		return nil, fmt.Errorf("get Argo CD Application %s/%s: %w", namespace, name, err)
	}
	return app, nil
}

func applicationName(mc *kaprov1alpha1.MemberCluster, appKey string) string {
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
