// Package capi implements a topology provider for Cluster API (CAPI) management clusters.
// It discovers workload clusters via cluster.x-k8s.io/v1beta1 Cluster resources and
// returns kubeconfigs stored in per-cluster Secrets.
//
// Auto-creates Kapro Environment CRDs when clusters reach ControlPlaneReady=True.
// Uses the dynamic client — no CAPI type imports to avoid version conflicts.
package capi

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

var clusterGVR = schema.GroupVersionResource{
	Group:    "cluster.x-k8s.io",
	Version:  "v1beta1",
	Resource: "clusters",
}

// compile-time check: Provider implements KCI Connector.
var _ pkgprovider.Connector = &Provider{}

// Provider connects to a CAPI management cluster and provides kubeconfigs for
// workload clusters managed by CAPI.
type Provider struct {
	mgmtClient    client.Client
	dynamicClient dynamic.Interface
	mgmtConfig    *rest.Config
}

// New constructs a Provider using the given management cluster config and client.
func New(mgmtConfig *rest.Config, mgmtClient client.Client) (*Provider, error) {
	dynClient, err := dynamic.NewForConfig(mgmtConfig)
	if err != nil {
		return nil, fmt.Errorf("capi.New: create dynamic client: %w", err)
	}
	return &Provider{
		mgmtClient:    mgmtClient,
		dynamicClient: dynClient,
		mgmtConfig:    mgmtConfig,
	}, nil
}

// Connect returns a *rest.Config for the target workload cluster by reading the
// per-cluster kubeconfig Secret from the CAPI management cluster.
//
// Secret name: <cluster-name>-kubeconfig, key: "value"
// Namespace: env.Spec.Provider.CAPI.Namespace, falling back to env.Namespace.
func (p *Provider) Connect(ctx context.Context, env *kaprov1alpha1.Environment) (*rest.Config, error) {
	if env == nil {
		return nil, fmt.Errorf("capi.Connect: environment is nil")
	}
	capiSpec := capiSpec(env)
	if capiSpec == nil {
		return nil, fmt.Errorf("capi.Connect: environment %s has no CAPI provider spec", env.Name)
	}

	ns := capiSpec.Namespace
	if ns == "" {
		ns = env.Namespace
	}
	clusterName := capiSpec.ClusterName
	if clusterName == "" {
		clusterName = env.Name
	}

	secretName := clusterName + "-kubeconfig"
	var secret corev1.Secret
	if err := p.mgmtClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: secretName}, &secret); err != nil {
		return nil, fmt.Errorf("capi.Connect: get kubeconfig secret %s/%s: %w", ns, secretName, err)
	}

	kubeconfigBytes, ok := secret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("capi.Connect: kubeconfig secret %s/%s missing key 'value'", ns, secretName)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("capi.Connect: parse kubeconfig for cluster %s: %w", clusterName, err)
	}
	return cfg, nil
}

// IsReachable checks if the workload cluster is reachable by attempting to connect
// and perform a basic API discovery call.
func (p *Provider) IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error) {
	cfg, err := p.Connect(ctx, env)
	if err != nil {
		return false, err
	}
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return false, fmt.Errorf("capi.IsReachable: create dynamic client: %w", err)
	}
	// A lightweight probe: list namespaces (empty list is fine — we just need a 200 OK).
	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	_, err = dynClient.Resource(nsGVR).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return false, nil //nolint:nilerr // unreachable cluster is not an error
	}
	return true, nil
}

// SyncEnvironments discovers CAPI Cluster resources where ControlPlaneReady=True
// and returns a slice of Kapro Environment objects ready for upsert.
//
// The caller is responsible for creating/updating these in Kubernetes.
func (p *Provider) SyncEnvironments(ctx context.Context, namespace string) ([]*kaprov1alpha1.Environment, error) {
	logger := log.FromContext(ctx).WithValues("namespace", namespace)

	clusterList, err := p.dynamicClient.Resource(clusterGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("capi.SyncEnvironments: list CAPI Clusters in %s: %w", namespace, err)
	}

	var envs []*kaprov1alpha1.Environment
	for _, item := range clusterList.Items {
		clusterName := item.GetName()

		if !isControlPlaneReady(item.Object) {
			logger.V(1).Info("cluster not ready, skipping", "cluster", clusterName)
			continue
		}

		env := buildEnvironment(clusterName, namespace, item.GetLabels())
		envs = append(envs, env)
		logger.Info("discovered CAPI cluster", "cluster", clusterName)
	}
	return envs, nil
}

// isControlPlaneReady returns true when the CAPI Cluster has condition
// type=ControlPlaneReady with status=True.
func isControlPlaneReady(obj map[string]interface{}) bool {
	conditions, ok := obj["status"].(map[string]interface{})
	if !ok {
		return false
	}
	condList, ok := conditions["conditions"].([]interface{})
	if !ok {
		return false
	}
	for _, c := range condList {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "ControlPlaneReady" && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// buildEnvironment constructs a Kapro Environment from a CAPI cluster name and labels.
func buildEnvironment(clusterName, namespace string, capiLabels map[string]string) *kaprov1alpha1.Environment {
	labels := map[string]string{
		"kapro.io/managed-by":     "capi",
		"kapro.io/capi-cluster":   clusterName,
	}
	// Propagate standard CAPI/Kapro labels from the Cluster object.
	for k, v := range capiLabels {
		labels[k] = v
	}

	return &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespace,
			Labels:    labels,
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
				CAPI: &kaprov1alpha1.CAPIProviderSpec{
					Namespace:   namespace,
					ClusterName: clusterName,
				},
			},
		},
	}
}

// capiSpec extracts the CAPI provider spec from an Environment.
func capiSpec(env *kaprov1alpha1.Environment) *kaprov1alpha1.CAPIProviderSpec {
	if env.Spec.Provider == nil {
		return nil
	}
	return env.Spec.Provider.CAPI
}
