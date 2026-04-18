// Package ocm implements a topology provider for Open Cluster Management (OCM) hub clusters.
// It discovers spoke clusters via cluster.open-cluster-management.io/v1 ManagedCluster
// resources and returns per-cluster kubeconfigs stored in hub Secrets.
//
// OCM conditions checked: ManagedClusterConditionAvailable=True
// Kubeconfig secret: <cluster>-admin-kubeconfig (key: kubeconfig)
//                 or <cluster>-kubeconfig        (key: value)  — fallback
//
// Uses the dynamic client — no OCM type imports to avoid version conflicts.
package ocm

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkgprovider "kapro.io/kapro/pkg/provider"
)

var managedClusterGVR = schema.GroupVersionResource{
	Group:    "cluster.open-cluster-management.io",
	Version:  "v1",
	Resource: "managedclusters",
}

// compile-time check: Provider implements KCI Connector.
var _ pkgprovider.Connector = &Provider{}

// Provider connects to an OCM hub cluster and discovers spoke clusters via
// ManagedCluster resources.
type Provider struct {
	hubClient     client.Client
	dynamicClient dynamic.Interface
	hubConfig     *rest.Config
}

// New constructs a Provider using the given OCM hub cluster config and client.
func New(hubConfig *rest.Config, hubClient client.Client) (*Provider, error) {
	dynClient, err := dynamic.NewForConfig(hubConfig)
	if err != nil {
		return nil, fmt.Errorf("ocm.New: create dynamic client: %w", err)
	}
	return &Provider{
		hubClient:     hubClient,
		dynamicClient: dynClient,
		hubConfig:     hubConfig,
	}, nil
}

// Connect returns a *rest.Config for a spoke cluster by reading its kubeconfig
// from a Secret on the OCM hub cluster.
//
// It tries two secret name conventions:
//  1. <cluster>-admin-kubeconfig, key "kubeconfig" (ACM/MCE convention)
//  2. <cluster>-kubeconfig, key "value"  (fallback)
func (p *Provider) Connect(ctx context.Context, env *kaprov1alpha1.Environment) (*rest.Config, error) {
	if env == nil {
		return nil, fmt.Errorf("ocm.Connect: environment is nil")
	}
	ocmSpec := ocmSpec(env)
	if ocmSpec == nil {
		return nil, fmt.Errorf("ocm.Connect: environment %s has no OCM provider spec", env.Name)
	}

	clusterName := ocmSpec.ClusterName
	if clusterName == "" {
		clusterName = env.Name
	}
	ns := ocmSpec.Namespace
	if ns == "" {
		ns = clusterName // OCM convention: cluster namespace == cluster name
	}

	// Try ACM convention first.
	kubeconfigBytes, err := p.readSecretKey(ctx, ns, clusterName+"-admin-kubeconfig", "kubeconfig")
	if err != nil {
		// Fall back to CAPI-style convention.
		kubeconfigBytes, err = p.readSecretKey(ctx, ns, clusterName+"-kubeconfig", "value")
		if err != nil {
			return nil, fmt.Errorf("ocm.Connect: no kubeconfig secret found for cluster %s in ns %s", clusterName, ns)
		}
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("ocm.Connect: parse kubeconfig for cluster %s: %w", clusterName, err)
	}
	return cfg, nil
}

// IsReachable checks whether the spoke cluster is available according to OCM
// (ManagedClusterConditionAvailable=True).
func (p *Provider) IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error) {
	if env == nil {
		return false, fmt.Errorf("ocm.IsReachable: environment is nil")
	}
	ocmSpec := ocmSpec(env)
	if ocmSpec == nil {
		return false, fmt.Errorf("ocm.IsReachable: environment %s has no OCM provider spec", env.Name)
	}

	clusterName := ocmSpec.ClusterName
	if clusterName == "" {
		clusterName = env.Name
	}

	obj, err := p.dynamicClient.Resource(managedClusterGVR).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("ocm.IsReachable: get ManagedCluster %s: %w", clusterName, err)
	}

	return isClusterAvailable(obj.Object), nil
}

// SyncEnvironments discovers OCM ManagedClusters where ManagedClusterConditionAvailable=True
// and returns a slice of Kapro Environment objects ready for upsert.
func (p *Provider) SyncEnvironments(ctx context.Context) ([]*kaprov1alpha1.Environment, error) {
	logger := log.FromContext(ctx)

	clusterList, err := p.dynamicClient.Resource(managedClusterGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("ocm.SyncEnvironments: list ManagedClusters: %w", err)
	}

	var envs []*kaprov1alpha1.Environment
	for _, item := range clusterList.Items {
		clusterName := item.GetName()

		if !isClusterAvailable(item.Object) {
			logger.V(1).Info("ManagedCluster not available, skipping", "cluster", clusterName)
			continue
		}

		env := buildEnvironment(clusterName, item.GetLabels())
		envs = append(envs, env)
		logger.Info("discovered OCM managed cluster", "cluster", clusterName)
	}
	return envs, nil
}

// readSecretKey reads a single key from a Kubernetes Secret on the hub cluster.
func (p *Provider) readSecretKey(ctx context.Context, namespace, secretName, key string) ([]byte, error) {
	var secret corev1.Secret
	if err := p.hubClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, &secret); err != nil {
		return nil, err
	}
	data, ok := secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s missing key %q", namespace, secretName, key)
	}
	return data, nil
}

// isClusterAvailable returns true when the ManagedCluster has condition
// ManagedClusterConditionAvailable=True.
func isClusterAvailable(obj map[string]interface{}) bool {
	status, ok := obj["status"].(map[string]interface{})
	if !ok {
		return false
	}
	conditions, ok := status["conditions"].([]interface{})
	if !ok {
		return false
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "ManagedClusterConditionAvailable" && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// buildEnvironment constructs a Kapro Environment from an OCM ManagedCluster.
func buildEnvironment(clusterName string, ocmLabels map[string]string) *kaprov1alpha1.Environment {
	labels := map[string]string{
		"kapro.io/managed-by":   "ocm",
		"kapro.io/ocm-cluster":  clusterName,
	}
	for k, v := range ocmLabels {
		labels[k] = v
	}

	return &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterName,
			Labels: labels,
		},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{
					Namespace:     "flux-system",
					OCIRepository: "ocs",
				},
			},
			Provider: &kaprov1alpha1.ProviderSpec{
				OCM: &kaprov1alpha1.OCMProviderSpec{
					ClusterName: clusterName,
					Namespace:   clusterName,
				},
			},
		},
	}
}

// ocmSpec extracts the OCM provider spec from an Environment.
func ocmSpec(env *kaprov1alpha1.Environment) *kaprov1alpha1.OCMProviderSpec {
	if env.Spec.Provider == nil {
		return nil
	}
	return env.Spec.Provider.OCM
}
