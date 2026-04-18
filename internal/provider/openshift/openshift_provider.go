// Package openshift implements a topology provider for Red Hat Advanced Cluster
// Management (ACM) and Hypershift hosted control planes.
//
// ACM is the downstream OpenShift distribution of Open Cluster Management (OCM).
// It uses the same cluster.open-cluster-management.io/v1 ManagedCluster CRD as OCM,
// but adds OpenShift-specific labels, cluster claims (version, console URL), and
// Hypershift hosted control planes via the hypershift.openshift.io/v1beta1 API.
//
// # Topology discovery
//
// Two sources are reconciled into Kapro Environments:
//
//  1. ACM ManagedClusters with label vendor=OpenShift (non-Hypershift OpenShift clusters)
//  2. Hypershift HostedClusters (hypershift.openshift.io/v1beta1) — managed hosted CPs
//
// # Kubeconfig resolution
//
// For ManagedClusters:   <clusterName>-admin-kubeconfig Secret, key "kubeconfig"
//
//	(fallback: <clusterName>-kubeconfig, key "value")
//
// For Hypershift:        <hostedClusterName>-admin-kubeconfig Secret in the
//
//	HostedCluster's namespace (key "kubeconfig"), or
//	<hostedClusterName>-kubeconfig (key "value")
//
// Uses dynamic client throughout — no OpenShift / Hypershift type imports.
package openshift

import (
	"context"
	"fmt"
	"strings"

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

// GVRs — no type imports needed.
var (
	managedClusterGVR = schema.GroupVersionResource{
		Group:    "cluster.open-cluster-management.io",
		Version:  "v1",
		Resource: "managedclusters",
	}

	hostedClusterGVR = schema.GroupVersionResource{
		Group:    "hypershift.openshift.io",
		Version:  "v1beta1",
		Resource: "hostedclusters",
	}
)

// compile-time check: Provider implements KCI Connector.
var _ pkgprovider.Connector = &Provider{}

// Provider connects to an ACM hub cluster and discovers OpenShift clusters
// (both standard and Hypershift hosted control planes) via ACM's ManagedCluster
// resources and Hypershift HostedCluster resources.
type Provider struct {
	hubClient     client.Client
	dynamicClient dynamic.Interface
	hubConfig     *rest.Config
}

// New constructs a Provider from the given ACM hub cluster REST config and client.
func New(hubConfig *rest.Config, hubClient client.Client) (*Provider, error) {
	dynClient, err := dynamic.NewForConfig(hubConfig)
	if err != nil {
		return nil, fmt.Errorf("openshift.New: create dynamic client: %w", err)
	}
	return &Provider{
		hubClient:     hubClient,
		dynamicClient: dynClient,
		hubConfig:     hubConfig,
	}, nil
}

// Connect returns a *rest.Config for a cluster by reading its admin kubeconfig
// from a Secret on the ACM hub.
//
// For Hypershift clusters (spec.HostedCluster=true) the secret is looked up in
// HostedClusterNamespace (default "clusters"); for regular ManagedClusters the
// secret is in the cluster's own namespace (clusterName).
func (p *Provider) Connect(ctx context.Context, env *kaprov1alpha1.Environment) (*rest.Config, error) {
	if env == nil {
		return nil, fmt.Errorf("openshift.Connect: environment is nil")
	}
	spec := openshiftSpec(env)
	if spec == nil {
		return nil, fmt.Errorf("openshift.Connect: environment %s has no OpenShift provider spec", env.Name)
	}

	clusterName := spec.ClusterName
	if clusterName == "" {
		clusterName = env.Name
	}

	var ns string
	if spec.HostedCluster {
		ns = spec.HostedClusterNamespace
		if ns == "" {
			ns = "clusters" // Hypershift default
		}
	} else {
		ns = spec.Namespace
		if ns == "" {
			ns = clusterName // ACM/OCM convention
		}
	}

	// Try ACM convention first: <cluster>-admin-kubeconfig, key "kubeconfig".
	kubeconfigBytes, err := p.readSecretKey(ctx, ns, clusterName+"-admin-kubeconfig", "kubeconfig")
	if err != nil {
		// Fallback: CAPI-style secret.
		kubeconfigBytes, err = p.readSecretKey(ctx, ns, clusterName+"-kubeconfig", "value")
		if err != nil {
			return nil, fmt.Errorf("openshift.Connect: no kubeconfig secret found for cluster %s in ns %s", clusterName, ns)
		}
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("openshift.Connect: parse kubeconfig for cluster %s: %w", clusterName, err)
	}
	return cfg, nil
}

// IsReachable checks whether the cluster is available.
//
// For standard OpenShift clusters: checks ManagedClusterConditionAvailable=True
// on the ACM hub.
// For Hypershift clusters: checks HostedCluster condition "Available=True".
func (p *Provider) IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error) {
	if env == nil {
		return false, fmt.Errorf("openshift.IsReachable: environment is nil")
	}
	spec := openshiftSpec(env)
	if spec == nil {
		return false, fmt.Errorf("openshift.IsReachable: environment %s has no OpenShift provider spec", env.Name)
	}

	clusterName := spec.ClusterName
	if clusterName == "" {
		clusterName = env.Name
	}

	if spec.HostedCluster {
		return p.isHostedClusterAvailable(ctx, clusterName, spec)
	}
	return p.isManagedClusterAvailable(ctx, clusterName)
}

// SyncEnvironments discovers all available OpenShift clusters from ACM
// (ManagedClusters with vendor=OpenShift) and Hypershift HostedClusters,
// and returns Kapro Environment objects ready for upsert.
func (p *Provider) SyncEnvironments(ctx context.Context) ([]*kaprov1alpha1.Environment, error) {
	logger := log.FromContext(ctx)

	var envs []*kaprov1alpha1.Environment

	// 1. Discover standard OpenShift clusters via ACM ManagedClusters.
	managedEnvs, err := p.discoverManagedClusters(ctx)
	if err != nil {
		// Log and continue — Hypershift discovery may still succeed.
		logger.Error(err, "failed to discover ACM ManagedClusters, continuing to Hypershift discovery")
	} else {
		envs = append(envs, managedEnvs...)
	}

	// 2. Discover Hypershift hosted clusters.
	hostedEnvs, err := p.discoverHostedClusters(ctx)
	if err != nil {
		logger.Error(err, "failed to discover Hypershift HostedClusters, continuing with ManagedCluster results")
	} else {
		envs = append(envs, hostedEnvs...)
	}

	logger.Info("openshift provider sync complete",
		"managedClusters", len(managedEnvs),
		"hostedClusters", len(hostedEnvs),
	)
	return envs, nil
}

// ─── private helpers ─────────────────────────────────────────────────────────

func (p *Provider) discoverManagedClusters(ctx context.Context) ([]*kaprov1alpha1.Environment, error) {
	logger := log.FromContext(ctx)

	// label selector: vendor=OpenShift filters out non-OpenShift managed clusters.
	clusterList, err := p.dynamicClient.Resource(managedClusterGVR).List(ctx, metav1.ListOptions{
		LabelSelector: "vendor=OpenShift",
	})
	if err != nil {
		return nil, fmt.Errorf("list ManagedClusters(vendor=OpenShift): %w", err)
	}

	var envs []*kaprov1alpha1.Environment
	for _, item := range clusterList.Items {
		clusterName := item.GetName()

		if !isManagedClusterAvailableObj(item.Object) {
			logger.V(1).Info("ManagedCluster not available, skipping", "cluster", clusterName)
			continue
		}

		labels := mergeLabelsWith(item.GetLabels(), map[string]string{
			"kapro.io/managed-by":        "openshift-acm",
			"kapro.io/openshift-cluster": clusterName,
		})

		// Extract OpenShift version from cluster claims if available.
		if v := clusterClaimValue(item.Object, "version.openshift.io"); v != "" {
			labels["kapro.io/openshift-version"] = v
		}

		env := buildEnvironment(clusterName, labels, false, "")
		envs = append(envs, env)
		logger.Info("discovered ACM OpenShift cluster", "cluster", clusterName)
	}
	return envs, nil
}

func (p *Provider) discoverHostedClusters(ctx context.Context) ([]*kaprov1alpha1.Environment, error) {
	logger := log.FromContext(ctx)

	clusterList, err := p.dynamicClient.Resource(hostedClusterGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		// CRD may not be installed — treat as soft failure.
		if strings.Contains(err.Error(), "no matches for kind") || strings.Contains(err.Error(), "no kind is registered") {
			logger.V(1).Info("Hypershift CRDs not installed, skipping HostedCluster discovery")
			return nil, nil
		}
		return nil, fmt.Errorf("list HostedClusters: %w", err)
	}

	var envs []*kaprov1alpha1.Environment
	for _, item := range clusterList.Items {
		clusterName := item.GetName()
		clusterNS := item.GetNamespace()

		if !isHostedClusterAvailableObj(item.Object) {
			logger.V(1).Info("HostedCluster not available, skipping", "cluster", clusterName, "namespace", clusterNS)
			continue
		}

		labels := mergeLabelsWith(item.GetLabels(), map[string]string{
			"kapro.io/managed-by":          "hypershift",
			"kapro.io/hypershift-cluster":  clusterName,
			"kapro.io/hypershift-namespace": clusterNS,
		})

		// Use namespace as part of env name for uniqueness across namespaces.
		envName := clusterNS + "-" + clusterName
		env := buildEnvironment(envName, labels, true, clusterNS)
		env.Spec.Provider.OpenShift.ClusterName = clusterName
		envs = append(envs, env)
		logger.Info("discovered Hypershift hosted cluster", "cluster", clusterName, "namespace", clusterNS)
	}
	return envs, nil
}

func (p *Provider) isManagedClusterAvailable(ctx context.Context, clusterName string) (bool, error) {
	obj, err := p.dynamicClient.Resource(managedClusterGVR).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("get ManagedCluster %s: %w", clusterName, err)
	}
	return isManagedClusterAvailableObj(obj.Object), nil
}

func (p *Provider) isHostedClusterAvailable(ctx context.Context, clusterName string, spec *kaprov1alpha1.OpenShiftProviderSpec) (bool, error) {
	ns := spec.HostedClusterNamespace
	if ns == "" {
		ns = "clusters"
	}
	obj, err := p.dynamicClient.Resource(hostedClusterGVR).Namespace(ns).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("get HostedCluster %s/%s: %w", ns, clusterName, err)
	}
	return isHostedClusterAvailableObj(obj.Object), nil
}

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

// ─── condition helpers ────────────────────────────────────────────────────────

// isManagedClusterAvailableObj checks ManagedClusterConditionAvailable=True.
func isManagedClusterAvailableObj(obj map[string]interface{}) bool {
	return hasConditionTrue(obj, "ManagedClusterConditionAvailable")
}

// isHostedClusterAvailableObj checks Hypershift HostedCluster condition Available=True.
func isHostedClusterAvailableObj(obj map[string]interface{}) bool {
	return hasConditionTrue(obj, "Available")
}

// hasConditionTrue returns true when a condition of the given type has status=True.
func hasConditionTrue(obj map[string]interface{}, condType string) bool {
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
		if cond["type"] == condType && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// clusterClaimValue extracts a ClusterClaim value from a ManagedCluster object.
// ACM stores claims under status.clusterClaims[].name / .value.
func clusterClaimValue(obj map[string]interface{}, claimName string) string {
	status, ok := obj["status"].(map[string]interface{})
	if !ok {
		return ""
	}
	claims, ok := status["clusterClaims"].([]interface{})
	if !ok {
		return ""
	}
	for _, c := range claims {
		claim, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if claim["name"] == claimName {
			v, _ := claim["value"].(string)
			return v
		}
	}
	return ""
}

// ─── environment builder ──────────────────────────────────────────────────────

// buildEnvironment constructs a Kapro Environment for an OpenShift cluster.
// isHosted=true sets HostedCluster + HostedClusterNamespace on the provider spec.
func buildEnvironment(envName string, labels map[string]string, isHosted bool, hostedNS string) *kaprov1alpha1.Environment {
	env := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   envName,
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
				OpenShift: &kaprov1alpha1.OpenShiftProviderSpec{
					ClusterName:            envName,
					Namespace:              envName,
					HostedCluster:          isHosted,
					HostedClusterNamespace: hostedNS,
				},
			},
		},
	}
	return env
}

// mergeLabelsWith merges base labels with overlay labels (overlay wins on conflict).
func mergeLabelsWith(base, overlay map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// openshiftSpec extracts the OpenShift provider spec from an Environment.
func openshiftSpec(env *kaprov1alpha1.Environment) *kaprov1alpha1.OpenShiftProviderSpec {
	if env.Spec.Provider == nil {
		return nil
	}
	return env.Spec.Provider.OpenShift
}
