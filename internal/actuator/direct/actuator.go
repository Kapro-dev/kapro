// Package direct implements hub-side direct Kubernetes apply for simple
// Deployment image promotions.
package direct

import (
	"context"
	"fmt"
	"path"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/actuator"
)

// Actuator implements direct hub-push Kubernetes delivery for generated raw
// manifest profiles. It updates explicitly selected Deployment container image
// fields and observes Deployment availability.
type Actuator struct {
	Client client.Client
}

var _ actuator.Actuator = (*Actuator)(nil)
var _ actuator.BackendObjectReporter = (*Actuator)(nil)

func (a *Actuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	if req.Cluster == nil {
		return fmt.Errorf("cluster is nil")
	}
	appKey := normalizeAppKey(req.AppKey)
	_, err := a.ApplyDelta(ctx, actuator.DeltaApplyRequest{
		Cluster:         req.Cluster,
		DesiredVersions: map[string]string{appKey: req.Version},
	})
	return err
}

func (a *Actuator) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("cluster is nil")
	}
	if a.Client == nil {
		return 0, fmt.Errorf("client is nil")
	}

	changed := 0
	for _, appKey := range sortedVersionKeys(req.DesiredVersions) {
		version := req.DesiredVersions[appKey]
		if version == "" {
			continue
		}
		deployment, containerName, err := a.getDeployment(ctx, req.Cluster, appKey)
		if err != nil {
			return changed, err
		}
		index, err := containerIndex(deployment, containerName)
		if err != nil {
			return changed, err
		}
		if deployment.Spec.Template.Spec.Containers[index].Image == version {
			continue
		}
		patch := client.MergeFrom(deployment.DeepCopy())
		deployment.Spec.Template.Spec.Containers[index].Image = version
		if err := a.Client.Patch(ctx, deployment, patch); err != nil {
			return changed, fmt.Errorf("patch Deployment %s/%s: %w", deployment.Namespace, deployment.Name, err)
		}
		changed++
	}
	return changed, nil
}

func (a *Actuator) IsConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, version, appKey string) (bool, error) {
	if cluster == nil {
		return false, fmt.Errorf("cluster is nil")
	}
	deployment, containerName, err := a.getDeployment(ctx, cluster, normalizeAppKey(appKey))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	index, err := containerIndex(deployment, containerName)
	if err != nil {
		return false, err
	}
	if deployment.Spec.Template.Spec.Containers[index].Image != version {
		return false, nil
	}
	desiredReplicas := int32(1)
	if deployment.Spec.Replicas != nil {
		desiredReplicas = *deployment.Spec.Replicas
	}
	if desiredReplicas == 0 {
		return true, nil
	}
	if deployment.Status.ObservedGeneration != 0 && deployment.Status.ObservedGeneration < deployment.Generation {
		return false, nil
	}
	if deployment.Status.UpdatedReplicas < desiredReplicas {
		return false, nil
	}
	if deployment.Status.ReadyReplicas < desiredReplicas {
		return false, nil
	}
	if deployment.Status.AvailableReplicas < desiredReplicas {
		return false, nil
	}
	return true, nil
}

func (a *Actuator) IsAllConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) (bool, error) {
	for _, appKey := range sortedVersionKeys(desiredVersions) {
		ok, err := a.IsConverged(ctx, cluster, desiredVersions[appKey], appKey)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (a *Actuator) Rollback(ctx context.Context, cluster *kaprov1alpha2.Cluster, previousVersion, appKey string) error {
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster: cluster,
		Version: previousVersion,
		AppKey:  appKey,
	})
}

func (a *Actuator) BackendObjects(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) ([]kaprov1alpha2.BackendObjectStatus, error) {
	statuses := make([]kaprov1alpha2.BackendObjectStatus, 0, len(desiredVersions))
	for _, appKey := range sortedVersionKeys(desiredVersions) {
		deployment, containerName, err := a.getDeployment(ctx, cluster, appKey)
		if err != nil {
			if apierrors.IsNotFound(err) {
				statuses = append(statuses, kaprov1alpha2.BackendObjectStatus{
					Kind:    "Deployment",
					Name:    directDeploymentName(cluster, appKey),
					Phase:   string(kaprov1alpha2.DeliveryPhasePending),
					Message: "Deployment not found",
				})
				continue
			}
			return nil, err
		}
		image := ""
		if index, err := containerIndex(deployment, containerName); err == nil {
			image = deployment.Spec.Template.Spec.Containers[index].Image
		}
		phase := string(kaprov1alpha2.DeliveryPhasePending)
		if image == desiredVersions[appKey] {
			phase = string(kaprov1alpha2.DeliveryPhaseApplying)
			if ok, _ := a.IsConverged(ctx, cluster, desiredVersions[appKey], appKey); ok {
				phase = string(kaprov1alpha2.DeliveryPhaseConverged)
			}
		}
		statuses = append(statuses, kaprov1alpha2.BackendObjectStatus{
			Kind:           "Deployment",
			Namespace:      deployment.Namespace,
			Name:           deployment.Name,
			DesiredVersion: desiredVersions[appKey],
			CurrentVersion: image,
			Phase:          phase,
		})
	}
	return statuses, nil
}

func (a *Actuator) getDeployment(ctx context.Context, cluster *kaprov1alpha2.Cluster, appKey string) (*appsv1.Deployment, string, error) {
	if a.Client == nil {
		return nil, "", fmt.Errorf("client is nil")
	}
	deployment := &appsv1.Deployment{}
	key := client.ObjectKey{
		Namespace: directNamespace(cluster),
		Name:      directDeploymentName(cluster, appKey),
	}
	if err := a.Client.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("get Deployment %s/%s: %w", key.Namespace, key.Name, err)
	}
	return deployment, directContainerName(cluster, appKey), nil
}

func directNamespace(cluster *kaprov1alpha2.Cluster) string {
	if cluster == nil {
		return "default"
	}
	return cluster.Spec.Delivery.Param("namespace", "default")
}

func directDeploymentName(cluster *kaprov1alpha2.Cluster, appKey string) string {
	appKey = normalizeAppKey(appKey)
	if cluster == nil {
		return appKey
	}
	if appKey != "default" {
		if name := cluster.Spec.Delivery.Param("deployment."+appKey, ""); name != "" {
			return name
		}
	}
	if name := cluster.Spec.Delivery.Param("deployment", ""); name != "" {
		return name
	}
	if manifestPath := cluster.Spec.Delivery.Param("manifestPath", ""); manifestPath != "" {
		if base := path.Base(manifestPath); base != "." && base != "/" {
			return base
		}
	}
	if appKey != "default" {
		return appKey
	}
	return cluster.Name
}

func directContainerName(cluster *kaprov1alpha2.Cluster, appKey string) string {
	appKey = normalizeAppKey(appKey)
	if cluster != nil && appKey != "default" {
		if name := cluster.Spec.Delivery.Param("container."+appKey, ""); name != "" {
			return name
		}
	}
	if cluster != nil {
		if name := cluster.Spec.Delivery.Param("container", ""); name != "" {
			return name
		}
	}
	return "app"
}

func containerIndex(deployment *appsv1.Deployment, containerName string) (int, error) {
	if deployment == nil {
		return 0, fmt.Errorf("deployment is nil")
	}
	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return 0, fmt.Errorf("deployment %s/%s has no containers", deployment.Namespace, deployment.Name)
	}
	for i, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == containerName {
			return i, nil
		}
	}
	if containerName == "app" && len(deployment.Spec.Template.Spec.Containers) == 1 {
		return 0, nil
	}
	return 0, fmt.Errorf("deployment %s/%s has no container %q", deployment.Namespace, deployment.Name, containerName)
}

func normalizeAppKey(appKey string) string {
	if appKey == "" {
		return "default"
	}
	return appKey
}

func sortedVersionKeys(versions map[string]string) []string {
	keys := make([]string, 0, len(versions))
	for key := range versions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func AvailableCondition() appsv1.DeploymentCondition {
	return appsv1.DeploymentCondition{
		Type:               appsv1.DeploymentAvailable,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}
}
