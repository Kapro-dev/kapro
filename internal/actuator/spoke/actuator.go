// Package spoke implements the Kapro Actuator Interface for spoke-local Flux.
//
// Instead of rendering HelmReleases on the hub with kubeConfig (push model),
// this actuator patches the OCIRepository tag on the spoke cluster. The spoke's
// own Flux controllers pull the bundle and reconcile HelmReleases locally.
//
// Convergence is checked by reading Flux Kustomization and HelmRelease status
// directly from the spoke via the kubeconfig secret.
//
// This gives full k9s visibility on each spoke: operators see wave Kustomizations,
// HelmRelease status, and the DAG ordering — not just opaque pods.
package spoke

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

var (
	ociRepoGVK = schema.GroupVersionKind{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "OCIRepository",
	}
	kustomizationGVK = schema.GroupVersionKind{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization",
	}
	helmReleaseGVK = schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
	}
)

// clientCacheTTL is how long a cached spoke client is reused before rebuilding.
const clientCacheTTL = 5 * time.Minute

type cachedClient struct {
	client    client.Client
	createdAt time.Time
}

// SpokeFluxActuator implements the Actuator interface by patching the
// OCIRepository tag on spoke clusters and checking local Flux status.
type SpokeFluxActuator struct {
	// HubClient is the controller-runtime client for the hub cluster.
	// Used to read kubeconfig secrets.
	HubClient client.Client

	// clientCache caches spoke clients by cluster name. TTL = clientCacheTTL.
	clientCache sync.Map // map[string]*cachedClient
}

var _ actuator.Actuator = (*SpokeFluxActuator)(nil)

// Apply patches the OCIRepository on the spoke cluster with the desired version tag.
func (a *SpokeFluxActuator) Apply(ctx context.Context, req actuator.ApplyRequest) error {
	l := log.FromContext(ctx)
	mc := req.Cluster
	if mc == nil {
		return fmt.Errorf("cluster is nil")
	}

	fluxSpec := mc.Spec.Actuator.Flux
	if fluxSpec == nil {
		return fmt.Errorf("MemberCluster %q has no flux actuator config", mc.Name)
	}

	spokeClient, err := a.spokeClient(ctx, mc)
	if err != nil {
		return fmt.Errorf("connect to spoke %s: %w", mc.Name, err)
	}

	ociRepoName := fluxSpec.OCIRepository
	if ociRepoName == "" {
		ociRepoName = mc.Name + "-bundle"
	}
	ns := fluxSpec.Namespace
	if ns == "" {
		ns = "flux-system"
	}

	// Read the OCIRepository on spoke.
	ociRepo := &unstructured.Unstructured{}
	ociRepo.SetGroupVersionKind(ociRepoGVK)
	if err := spokeClient.Get(ctx, client.ObjectKey{Name: ociRepoName, Namespace: ns}, ociRepo); err != nil {
		return fmt.Errorf("get OCIRepository %s/%s on spoke %s: %w", ns, ociRepoName, mc.Name, err)
	}

	// Patch the tag.
	patch := client.MergeFrom(ociRepo.DeepCopy())
	spec, _ := ociRepo.Object["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
		ociRepo.Object["spec"] = spec
	}
	ref, _ := spec["ref"].(map[string]any)
	if ref == nil {
		ref = map[string]any{}
		spec["ref"] = ref
	}
	ref["tag"] = req.Version

	if err := spokeClient.Patch(ctx, ociRepo, patch); err != nil {
		return fmt.Errorf("patch OCIRepository %s tag=%s on spoke %s: %w", ociRepoName, req.Version, mc.Name, err)
	}

	l.Info("patched OCIRepository on spoke",
		"cluster", mc.Name, "ociRepository", ociRepoName, "tag", req.Version)
	return nil
}

// ApplyDelta patches the OCIRepository tag with the primary version.
// For spoke-local Flux, a single OCI bundle contains all component versions,
// so there's only one tag to patch.
func (a *SpokeFluxActuator) ApplyDelta(ctx context.Context, req actuator.DeltaApplyRequest) (int, error) {
	if req.Cluster == nil {
		return 0, fmt.Errorf("cluster is nil")
	}

	// Use the first version found — the bundle is tagged with the primary version.
	var primaryVersion string
	for _, v := range req.DesiredVersions {
		primaryVersion = v
		break
	}
	if primaryVersion == "" {
		return 0, nil
	}

	err := a.Apply(ctx, actuator.ApplyRequest{
		Cluster: req.Cluster,
		Version: primaryVersion,
	})
	if err != nil {
		return 0, err
	}
	return len(req.DesiredVersions), nil
}

// IsConverged checks if all Flux Kustomizations and HelmReleases on the spoke
// are Ready. This gives the hub a complete view of spoke-side deployment status.
func (a *SpokeFluxActuator) IsConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, appKey, version string) (bool, error) {
	return a.isAllSpokeReady(ctx, mc)
}

// IsAllConverged checks convergence for all desired versions.
func (a *SpokeFluxActuator) IsAllConverged(ctx context.Context, mc *kaprov1alpha1.MemberCluster, desiredVersions map[string]string) (bool, error) {
	return a.isAllSpokeReady(ctx, mc)
}

// Rollback sets the OCIRepository tag back to a previous version.
func (a *SpokeFluxActuator) Rollback(ctx context.Context, mc *kaprov1alpha1.MemberCluster, previousVersion, appKey string) error {
	return a.Apply(ctx, actuator.ApplyRequest{
		Cluster: mc,
		Version: previousVersion,
	})
}

// isAllSpokeReady connects to the spoke and checks that all Kustomizations
// and HelmReleases managed by Kapro are Ready.
func (a *SpokeFluxActuator) isAllSpokeReady(ctx context.Context, mc *kaprov1alpha1.MemberCluster) (bool, error) {
	spokeClient, err := a.spokeClient(ctx, mc)
	if err != nil {
		return false, fmt.Errorf("connect to spoke %s: %w", mc.Name, err)
	}

	ns := "flux-system"
	if mc.Spec.Actuator.Flux != nil && mc.Spec.Actuator.Flux.Namespace != "" {
		ns = mc.Spec.Actuator.Flux.Namespace
	}

	// Check all Kustomizations with kapro.io/managed-by label.
	ksList := &unstructured.UnstructuredList{}
	ksList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList",
	})
	if err := spokeClient.List(ctx, ksList,
		client.InNamespace(ns),
		client.MatchingLabels{"kapro.io/managed-by": mc.Name},
	); err != nil {
		// If Kustomization CRD not found or no resources, fall through to HR check.
		if !isNotFoundOrEmpty(err) {
			return false, fmt.Errorf("list Kustomizations on spoke %s: %w", mc.Name, err)
		}
	}
	for _, ks := range ksList.Items {
		if !hasReadyCondition(&ks) {
			return false, nil
		}
	}

	// Check all HelmReleases with kapro.io/managed-by label.
	hrList := &unstructured.UnstructuredList{}
	hrList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList",
	})
	if err := spokeClient.List(ctx, hrList,
		client.InNamespace(ns),
		client.MatchingLabels{"kapro.io/managed-by": mc.Name},
	); err != nil {
		return false, fmt.Errorf("list HelmReleases on spoke %s: %w", mc.Name, err)
	}
	if len(hrList.Items) == 0 {
		return false, nil // No HRs yet — not converged.
	}
	for _, hr := range hrList.Items {
		if !hasReadyCondition(&hr) {
			return false, nil
		}
	}

	return true, nil
}

// spokeClient returns a cached controller-runtime client for the spoke cluster.
// Clients are cached for clientCacheTTL to avoid repeated kubeconfig parsing
// and REST client creation (expensive at 150 clusters).
func (a *SpokeFluxActuator) spokeClient(ctx context.Context, mc *kaprov1alpha1.MemberCluster) (client.Client, error) {
	// Check cache.
	if cached, ok := a.clientCache.Load(mc.Name); ok {
		cc := cached.(*cachedClient)
		if time.Since(cc.createdAt) < clientCacheTTL {
			// Quick health probe — if auth fails, invalidate cache.
			probe := &unstructured.Unstructured{}
			probe.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
			if err := cc.client.Get(ctx, client.ObjectKey{Name: "flux-system"}, probe); err == nil {
				return cc.client, nil
			}
			// Auth failed — rebuild client.
			a.clientCache.Delete(mc.Name)
		} else {
			a.clientCache.Delete(mc.Name)
		}
	}

	// Build new client.
	c, err := a.buildSpokeClient(ctx, mc)
	if err != nil {
		return nil, err
	}

	a.clientCache.Store(mc.Name, &cachedClient{
		client:    c,
		createdAt: time.Now(),
	})
	return c, nil
}

func (a *SpokeFluxActuator) buildSpokeClient(ctx context.Context, mc *kaprov1alpha1.MemberCluster) (client.Client, error) {
	// Find kubeconfig secret by label or convention.
	secretList := &corev1.SecretList{}
	if err := a.HubClient.List(ctx, secretList,
		client.InNamespace("flux-system"),
		client.MatchingLabels{"kapro.io/cluster": mc.Name},
	); err == nil && len(secretList.Items) > 0 {
		return clientFromSecret(&secretList.Items[0], mc.Name)
	}

	// Fallback: convention-based name.
	var secret corev1.Secret
	if err := a.HubClient.Get(ctx, client.ObjectKey{
		Name:      mc.Name + "-kubeconfig",
		Namespace: "flux-system",
	}, &secret); err != nil {
		return nil, fmt.Errorf("kubeconfig secret for %s not found: %w", mc.Name, err)
	}
	return clientFromSecret(&secret, mc.Name)
}

func clientFromSecret(secret *corev1.Secret, clusterName string) (client.Client, error) {
	kubeconfigData := secret.Data["value"]
	if len(kubeconfigData) == 0 {
		return nil, fmt.Errorf("kubeconfig secret %s has no 'value' key", secret.Name)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig for %s: %w", clusterName, err)
	}

	return client.New(restConfig, client.Options{})
}

// hasReadyCondition checks if an unstructured object has Ready=True.
func hasReadyCondition(obj *unstructured.Unstructured) bool {
	status, _ := obj.Object["status"].(map[string]any)
	if status == nil {
		return false
	}
	conditions, _ := status["conditions"].([]any)
	for _, c := range conditions {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		if fmt.Sprintf("%v", cm["type"]) == "Ready" && fmt.Sprintf("%v", cm["status"]) == "True" {
			return true
		}
	}
	return false
}

func isNotFoundOrEmpty(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "not found") ||
		strings.Contains(err.Error(), "no matches for kind"))
}
