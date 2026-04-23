package main

// kapro spoke — manages workload (spoke) cluster installation.
//
// spoke install: full automation for pipelines (reads bootstrap Secret from hub).
// spoke join:   manual kubeadm-like fallback — takes --token directly, no hub access.
//
// Both commands follow the Flux pattern: the installer creates all spoke resources
// declaratively; the cluster-controller binary itself is stateless.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func newSpokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spoke",
		Short: "Manage workload cluster (spoke) installation",
	}
	cmd.AddCommand(newSpokeInstallCmd())
	cmd.AddCommand(newSpokeJoinCmd())
	return cmd
}

func newSpokeInstallCmd() *cobra.Command {
	var (
		clusterName       string
		hubKubeconfig     string
		spokeKubeconfig   string
		hubURL            string
		image             string
		gcpServiceAccount string
		export            bool
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install kapro-cluster-controller on a workload cluster (reads hub bootstrap Secret)",
		Long: `Install reads the bootstrap kubeconfig Secret from the hub cluster, extracts the
bootstrap SA token, and applies all spoke manifests declaratively.

This is the automated path — use it in pipelines where you have kubeconfig
access to both hub and spoke. For air-gapped or multi-team setups, use
'kapro spoke join' instead (takes --token directly, no hub access needed).

Mirrors the Flux pattern: installer owns namespace + RBAC + Deployment;
the controller binary itself makes no assumptions about pre-existing resources.

Example:
  # Bootstrap the cluster registration on hub
  kapro cluster bootstrap --name spoke-de --labels tier=dev

  # Switch to spoke context (like flux install)
  kubectl config use-context spoke-de

  # Install — reads bootstrap Secret from hub automatically
  kapro spoke install --cluster-name spoke-de --hub-url https://172.18.0.11:6443`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpokeInstall(cmd.Context(), clusterName, hubKubeconfig, spokeKubeconfig, hubURL, image, gcpServiceAccount, export)
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster-name", "", "Cluster name (must match the MemberCluster name on hub)")
	cmd.Flags().StringVar(&hubKubeconfig, "hub-kubeconfig", "", "Path to hub kubeconfig (defaults to KUBECONFIG / ~/.kube/config)")
	cmd.Flags().StringVar(&spokeKubeconfig, "spoke-kubeconfig", "", "Path to spoke kubeconfig (defaults to current KUBECONFIG context, like flux install)")
	cmd.Flags().StringVar(&hubURL, "hub-url", "", "Spoke-reachable hub API URL (e.g. https://172.18.0.11:6443 for kind)")
	cmd.Flags().StringVar(&image, "image", "ghcr.io/vinnxcapital-gif/kapro/cluster-controller:latest", "cluster-controller container image")
	cmd.Flags().StringVar(&gcpServiceAccount, "gcp-service-account", "", "GCP service account email for Workload Identity (GKE only, e.g. kapro-cc@project.iam.gserviceaccount.com)")
	cmd.Flags().BoolVar(&export, "export", false, "Print manifests to stdout instead of applying them")

	_ = cmd.MarkFlagRequired("cluster-name")

	return cmd
}

func runSpokeInstall(ctx context.Context, clusterName, hubKubeconfigPath, spokeKubeconfigPath, hubURL, image, gcpServiceAccount string, export bool) error {
	// ── 1. Load hub client ──────────────────────────────────────────────────────
	hubLoadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if hubKubeconfigPath != "" {
		hubLoadingRules.ExplicitPath = hubKubeconfigPath
	}
	hubCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		hubLoadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return fmt.Errorf("load hub kubeconfig: %w", err)
	}

	hubClient, err := client.New(hubCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create hub client: %w", err)
	}

	// ── 3. Read hub CA from hub kubeconfig ──────────────────────────────────────
	hubRawCA := hubCfg.CAData
	if len(hubRawCA) == 0 && hubCfg.CAFile != "" {
		hubRawCA, _ = os.ReadFile(hubCfg.CAFile)
	}
	hubCABundle := base64.StdEncoding.EncodeToString(hubRawCA)

	// If --hub-url not set, fall back to the server in the hub kubeconfig.
	effectiveHubURL := hubURL
	if effectiveHubURL == "" {
		effectiveHubURL = hubCfg.Host
	}

	var bootstrapToken string
	if gcpServiceAccount == "" {
		// ── 2. Read bootstrap kubeconfig Secret from hub (generic mode only) ────
		bootstrapSecretName := "kapro-bootstrap-kubeconfig-" + clusterName
		var bootstrapSecret corev1.Secret
		if err := hubClient.Get(ctx, types.NamespacedName{
			Namespace: "kapro-system",
			Name:      bootstrapSecretName,
		}, &bootstrapSecret); err != nil {
			return fmt.Errorf("read bootstrap Secret %q from hub: %w\n"+
				"  Hint: run `kapro cluster bootstrap --name %s` first", bootstrapSecretName, err, clusterName)
		}
		bootstrapKubeconfig := bootstrapSecret.Data["kubeconfig"]
		if len(bootstrapKubeconfig) == 0 {
			return fmt.Errorf("bootstrap Secret %q has no 'kubeconfig' key", bootstrapSecretName)
		}

		// ── 4. Extract bootstrap SA token ────────────────────────────────────────
		bootstrapToken, err = extractTokenFromKubeconfig(bootstrapKubeconfig)
		if err != nil {
			return fmt.Errorf("extract bootstrap token from Secret %q: %w", bootstrapSecretName, err)
		}
	} else {
		// ── GCP mode: apply hub-side RBAC before spoke manifests ─────────────────
		fmt.Printf("  Applying hub RBAC for GSA %q ...\n", gcpServiceAccount)
		for _, obj := range buildHubGCPManifests(clusterName, gcpServiceAccount) {
			if err := applyObject(ctx, hubClient, obj); err != nil {
				return fmt.Errorf("apply hub GCP RBAC %T %q: %w", obj, obj.(metav1.Object).GetName(), err)
			}
			fmt.Printf("  ✅ hub: %T/%s\n", obj, obj.(metav1.Object).GetName())
		}
	}

	// ── 5. Build manifests ──────────────────────────────────────────────────────
	manifests := buildSpokeManifests(clusterName, effectiveHubURL, hubCABundle, bootstrapToken, image, gcpServiceAccount)

	if export {
		return printManifests(manifests)
	}

	// ── 6. Apply to spoke ───────────────────────────────────────────────────────
	// Mirrors flux install: if no --spoke-kubeconfig given, uses the current KUBECONFIG context.
	spokeLoadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if spokeKubeconfigPath != "" {
		spokeLoadingRules.ExplicitPath = spokeKubeconfigPath
	}
	spokeCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		spokeLoadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return fmt.Errorf("load spoke kubeconfig: %w", err)
	}

	// Use a full scheme that includes apps/v1 and rbac/v1 for applying.
	spokeScheme := runtime.NewScheme()
	_ = clientgoSchemeForSpoke(spokeScheme)

	spokeClient, err := client.New(spokeCfg, client.Options{Scheme: spokeScheme})
	if err != nil {
		return fmt.Errorf("create spoke client: %w", err)
	}

	for _, obj := range manifests {
		if err := applyObject(ctx, spokeClient, obj); err != nil {
			return fmt.Errorf("apply %T %q: %w", obj, obj.(metav1.Object).GetName(), err)
		}
		fmt.Printf("  ✅ %T/%s\n", obj, obj.(metav1.Object).GetName())
	}

	fmt.Printf("\n✅ kapro-cluster-controller installed on %s\n", clusterName)
	fmt.Printf("   Monitor: kubectl --kubeconfig %s get deploy kapro-cluster-controller -n kapro-system\n", spokeKubeconfigPath)
	return nil
}

// buildSpokeManifests returns the ordered list of Kubernetes objects to apply to the spoke.
//
// Generic mode (gcpServiceAccount == ""):
//   - Includes kapro-bootstrap-token Secret (raw hub SA token for the CSR dance).
//   - Deployment uses KAPRO_BOOTSTRAP_TOKEN env var; provider defaults to "generic".
//
// GCP / Workload Identity mode (gcpServiceAccount != ""):
//   - Omits the bootstrap token Secret entirely (no sensitive credential on spoke).
//   - Deployment uses KAPRO_PROVIDER=gcp; KSA is annotated for WI federation.
func buildSpokeManifests(clusterName, hubURL, hubCABundle, bootstrapToken, image, gcpServiceAccount string) []client.Object {
	labels := map[string]string{
		"app.kubernetes.io/name":       "kapro-cluster-controller",
		"app.kubernetes.io/managed-by": "kapro",
		"kapro.io/cluster":             clusterName,
	}

	one := int32(1)

	ksaMeta := metav1.ObjectMeta{
		Name:      "kapro-cluster-controller",
		Namespace: "kapro-system",
		Labels:    labels,
	}
	if gcpServiceAccount != "" {
		ksaMeta.Annotations = map[string]string{
			"iam.gke.io/gcp-service-account": gcpServiceAccount,
		}
	}

	// Provider-specific Deployment env var: bootstrap token (generic) or provider tag (gcp).
	var providerEnv corev1.EnvVar
	if gcpServiceAccount != "" {
		providerEnv = corev1.EnvVar{Name: "KAPRO_PROVIDER", Value: "gcp"}
	} else {
		providerEnv = corev1.EnvVar{
			Name: "KAPRO_BOOTSTRAP_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "kapro-bootstrap-token"},
					Key:                  "token",
					Optional:             boolPtr(true), // absent after first bootstrap cycle is fine
				},
			},
		}
	}

	// Start with mandatory objects.
	manifests := []client.Object{
		// 1. Namespace
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "kapro-system",
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kapro"},
			},
		},
	}

	// 2. Bootstrap token Secret — generic mode only.
	//    GCP mode authenticates via Workload Identity; no token Secret is needed.
	if gcpServiceAccount == "" {
		manifests = append(manifests, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kapro-bootstrap-token",
				Namespace: "kapro-system",
				Labels:    labels,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte(bootstrapToken)},
		})
	}

	manifests = append(manifests,
		// 3. Hub connection ConfigMap
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kapro-hub-config",
				Namespace: "kapro-system",
				Labels:    labels,
			},
			Data: map[string]string{
				"clusterName": clusterName,
				"hubURL":      hubURL,
				"hubCABundle": hubCABundle,
			},
		},

		// 4. ServiceAccount — GKE WI annotation added when --gcp-service-account is set.
		&corev1.ServiceAccount{
			ObjectMeta: ksaMeta,
		},

		// 5. ClusterRole — spoke-side permissions for the cluster-controller
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "kapro:cluster-controller",
				Labels: labels,
			},
			Rules: []rbacv1.PolicyRule{
				// Hub credentials secret (store mTLS cert after bootstrap)
				{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
				// Flux OCIRepository — read desired version published by kapro wave
				{APIGroups: []string{"source.toolkit.fluxcd.io"}, Resources: []string{"ocirepositories"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
				// Flux Kustomization — read workload health for heartbeat
				{APIGroups: []string{"kustomize.toolkit.fluxcd.io"}, Resources: []string{"kustomizations"}, Verbs: []string{"get", "list", "watch"}},
			},
		},

		// 6. ClusterRoleBinding
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "kapro:cluster-controller",
				Labels: labels,
			},
			RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "kapro:cluster-controller"},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "kapro-cluster-controller", Namespace: "kapro-system"}},
		},

		// 7. Deployment
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kapro-cluster-controller",
				Namespace: "kapro-system",
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "kapro-cluster-controller"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "kapro-cluster-controller"}},
					Spec: corev1.PodSpec{
						ServiceAccountName: "kapro-cluster-controller",
						Containers: []corev1.Container{
							{
								Name:  "cluster-controller",
								Image: image,
								Env: []corev1.EnvVar{
									{Name: "KAPRO_TARGET", ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: "kapro-hub-config"},
											Key:                  "clusterName",
										},
									}},
									{Name: "KAPRO_CONTROL_PLANE_URL", ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: "kapro-hub-config"},
											Key:                  "hubURL",
										},
									}},
									{Name: "KAPRO_CONTROL_PLANE_CA_BUNDLE", ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: "kapro-hub-config"},
											Key:                  "hubCABundle",
										},
									}},
									providerEnv,
								},
							},
						},
					},
				},
			},
		},
	)

	return manifests
}

// buildHubGCPManifests returns the hub-side ClusterRole and ClusterRoleBinding
// required when a spoke pod authenticates to the hub via GCP Workload Identity.
//
// These mirror the RBAC objects created by csrapproval_controller.go:ensureClusterRBAC
// for the generic (CSR) path, but bind to the GSA email (kind: User) instead of the
// x509 CN. The ClusterRoleBinding uses suffix ":gcp" to coexist with the generic CRB.
func buildHubGCPManifests(clusterName, gcpServiceAccount string) []client.Object {
	roleName := "kapro:cluster-controller:" + clusterName
	labels := map[string]string{
		"kapro.io/managed-by": "kapro",
		"kapro.io/cluster":    clusterName,
	}
	return []client.Object{
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: roleName, Labels: labels},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"kapro.io"},
					Resources: []string{"memberclusters"},
					Verbs:     []string{"get", "update", "patch"},
				},
				{
					APIGroups: []string{"kapro.io"},
					Resources: []string{"memberclusters/status"},
					Verbs:     []string{"update", "patch"},
				},
				{
					APIGroups: []string{"certificates.k8s.io"},
					Resources: []string{"certificatesigningrequests"},
					Verbs:     []string{"create", "get", "watch"},
				},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: roleName + ":gcp",
				Labels: map[string]string{
					"kapro.io/managed-by": "kapro",
					"kapro.io/cluster":    clusterName,
					"kapro.io/provider":   "gcp",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     roleName,
			},
			// Subject is the GSA email; GKE API server maps WI tokens to the GSA identity.
			Subjects: []rbacv1.Subject{{Kind: "User", Name: gcpServiceAccount}},
		},
	}
}

// extractTokenFromKubeconfig parses a kubeconfig YAML and returns the Bearer token.
// The bootstrap kubeconfig generated by the hub operator always uses a static token.
func extractTokenFromKubeconfig(kubeconfig []byte) (string, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	if restCfg.BearerToken == "" {
		return "", fmt.Errorf("kubeconfig does not contain a Bearer token (exec/tokenFile credentials are not supported)")
	}
	return restCfg.BearerToken, nil
}

// ─── kapro spoke join ─────────────────────────────────────────────────────────

// newSpokeJoinCmd returns the 'kapro spoke join' command — the manual kubeadm-like
// fallback for air-gapped clusters or multi-team setups where a pipeline cannot
// reach the spoke API server directly.
//
// Usage (token comes from 'kapro cluster bootstrap' output or 'kapro cluster join --print-join-command'):
//
//	kapro spoke join \
//	  --name spoke-de \
//	  --hub https://172.18.0.11:6443 \
//	  --token eyJhbGci... \
//	  --ca-bundle LS0t...
func newSpokeJoinCmd() *cobra.Command {
	var (
		clusterName       string
		hubURL            string
		token             string
		caBundle          string
		spokeKubeconfig   string
		image             string
		gcpServiceAccount string
		export            bool
	)

	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join a workload cluster to Kapro using a bootstrap token (no hub access needed)",
		Long: `Join installs the kapro-cluster-controller on a spoke cluster using credentials
provided directly — no access to the hub cluster is required.

This is the manual fallback for:
  - Air-gapped fleets
  - Different teams managing hub vs spoke
  - CI pipelines without hub kubeconfig access

The token and ca-bundle come from 'kapro cluster bootstrap' output or
'kapro cluster join --print-join-command'.

Example:
  # Get the join command from hub admin (runs once)
  kapro cluster bootstrap --name spoke-de --labels tier=dev

  # On the spoke cluster (paste the join command from above)
  kubectl config use-context spoke-de
  kapro spoke join \
    --name spoke-de \
    --hub https://hub.internal.com \
    --token eyJhbGci... \
    --ca-bundle LS0tLS1CRUdJTi...`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpokeJoin(cmd.Context(), clusterName, hubURL, token, caBundle, spokeKubeconfig, image, gcpServiceAccount, export)
		},
	}

	cmd.Flags().StringVar(&clusterName, "name", "", "Cluster name (must match the MemberCluster name on hub)")
	cmd.Flags().StringVar(&hubURL, "hub", "", "Hub API server URL reachable from the spoke (e.g. https://hub.internal.com)")
	cmd.Flags().StringVar(&token, "token", "", "Bootstrap SA token (from `kapro cluster bootstrap` output)")
	cmd.Flags().StringVar(&caBundle, "ca-bundle", "", "Hub CA certificate (base64-encoded PEM)")
	cmd.Flags().StringVar(&spokeKubeconfig, "spoke-kubeconfig", "", "Spoke kubeconfig (defaults to current KUBECONFIG context)")
	cmd.Flags().StringVar(&image, "image", "ghcr.io/vinnxcapital-gif/kapro/cluster-controller:latest", "cluster-controller container image")
	cmd.Flags().StringVar(&gcpServiceAccount, "gcp-service-account", "", "GCP service account email for Workload Identity (GKE only, e.g. kapro-cc@project.iam.gserviceaccount.com)")
	cmd.Flags().BoolVar(&export, "export", false, "Print manifests to stdout instead of applying them")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("hub")
	// --token is required for generic mode but optional when --gcp-service-account is set.
	// Validation is done inside runSpokeJoin.
	_ = cmd.MarkFlagRequired("ca-bundle")

	return cmd
}

func runSpokeJoin(ctx context.Context, clusterName, hubURL, token, caBundle, spokeKubeconfigPath, image, gcpServiceAccount string, export bool) error {
	if gcpServiceAccount == "" && token == "" {
		return fmt.Errorf("--token is required unless --gcp-service-account is set (GCP Workload Identity mode)")
	}
	if gcpServiceAccount != "" && token != "" {
		fmt.Printf("⚠️  --token is ignored in GCP Workload Identity mode; the spoke pod authenticates via WI.\n")
	}

	manifests := buildSpokeManifests(clusterName, hubURL, caBundle, token, image, gcpServiceAccount)

	if gcpServiceAccount != "" {
		fmt.Printf("⚠️  GCP mode: hub-side RBAC for %q must exist before the spoke pod starts.\n", gcpServiceAccount)
		fmt.Printf("   Run 'kapro cluster join --gcp-service-account %s ...' or 'kapro spoke install --gcp-service-account %s ...' from a machine with hub access.\n",
			gcpServiceAccount, gcpServiceAccount)
	}

	if export {
		return printManifests(manifests)
	}

	spokeLoadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if spokeKubeconfigPath != "" {
		spokeLoadingRules.ExplicitPath = spokeKubeconfigPath
	}
	spokeCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		spokeLoadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return fmt.Errorf("load spoke kubeconfig: %w", err)
	}

	spokeScheme := runtime.NewScheme()
	_ = clientgoSchemeForSpoke(spokeScheme)

	spokeClient, err := client.New(spokeCfg, client.Options{Scheme: spokeScheme})
	if err != nil {
		return fmt.Errorf("create spoke client: %w", err)
	}

	for _, obj := range manifests {
		if err := applyObject(ctx, spokeClient, obj); err != nil {
			return fmt.Errorf("apply %T %q: %w", obj, obj.(metav1.Object).GetName(), err)
		}
		fmt.Printf("  ✅ %T/%s\n", obj, obj.(metav1.Object).GetName())
	}

	fmt.Printf("\n✅ kapro-cluster-controller joining %s\n", clusterName)
	fmt.Printf("   Monitor: kubectl get deploy kapro-cluster-controller -n kapro-system\n")
	fmt.Printf("   Status:  kubectl get membercluster %s\n", clusterName)
	return nil
}

// applyObject creates or updates a single object using server-side apply semantics.
func applyObject(ctx context.Context, c client.Client, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, obj)
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return c.Update(ctx, obj)
}

// printManifests serialises objects to YAML separated by "---".
func printManifests(objs []client.Object) error {
	s := json.NewSerializerWithOptions(json.DefaultMetaFactory, nil, nil,
		json.SerializerOptions{Yaml: true, Pretty: true, Strict: false})
	for i, obj := range objs {
		if i > 0 {
			fmt.Println("---")
		}
		if err := s.Encode(obj, os.Stdout); err != nil {
			return fmt.Errorf("encode %T: %w", obj, err)
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

// clientgoSchemeForSpoke builds a scheme with all types used by spoke manifests.
func clientgoSchemeForSpoke(s *runtime.Scheme) error {
	if err := corev1.AddToScheme(s); err != nil {
		return err
	}
	if err := appsv1.AddToScheme(s); err != nil {
		return err
	}
	if err := rbacv1.AddToScheme(s); err != nil {
		return err
	}
	return kaprov1alpha1.AddToScheme(s)
}
