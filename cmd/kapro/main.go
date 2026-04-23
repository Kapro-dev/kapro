// Command kapro is the CLI for the Kapro promotion engine.
//
// Usage:
//
//	kapro cluster bootstrap --name <cluster-name> [--labels key=value,...]
//	kapro get releases [-n namespace]
//	kapro get syncs [-n namespace]
//	kapro approve <sync-name> [-n namespace] [--comment text]
//	kapro rollback <release-name> --to <digest> [-n namespace]
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

func main() {
	root := &cobra.Command{
		Use:   "kapro",
		Short: "The Canonical Promotion Layer for Kubernetes",
		Long: `kapro — multi-cluster progressive delivery engine.

Passes versions forward. Through environments. Across clusters. In waves.`,
	}

	root.AddCommand(newClusterCmd())
	root.AddCommand(newSpokeCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newApproveCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newArtifactCmd())
	root.AddCommand(newReleaseCmd())
	root.AddCommand(newPromoteCmd())
	root.AddCommand(newWorldCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage cluster registrations",
	}
	cmd.AddCommand(newBootstrapCmd())
	cmd.AddCommand(newClusterJoinCmd())
	return cmd
}

func newBootstrapCmd() *cobra.Command {
	var (
		clusterName string
		namespace   string
		labelsRaw   []string
		ttl         time.Duration
		kubeconfig  string
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Register a workload cluster with Kapro",
		Long: `Bootstrap creates a MemberCluster CR on the management cluster with a bootstrap token.
The Kapro operator processes it, creates a scoped ServiceAccount + RBAC, and
writes the cluster credentials to a Secret named kapro-cluster-<name>-credentials.

Steps:
  1. Run this command on a machine with access to the MANAGEMENT cluster kubeconfig.
  2. Copy the printed token to the WORKLOAD cluster.
  3. Start kapro-cluster-controller with --bootstrap-token=<token>.

Example:
  kapro cluster bootstrap --name de-prod --labels env=prod,region=eu-west`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(cmd.Context(), clusterName, namespace, labelsRaw, ttl, kubeconfig)
		},
	}

	cmd.Flags().StringVar(&clusterName, "name", "", "Cluster name (required, must match MemberCluster name)")
	cmd.Flags().StringVar(&namespace, "namespace", "kapro-system", "Namespace for bootstrap (unused — MemberCluster is cluster-scoped)")
	cmd.Flags().StringArrayVar(&labelsRaw, "labels", nil, "Labels for the MemberCluster (key=value)")
	cmd.Flags().DurationVar(&ttl, "ttl", 24*time.Hour, "Token TTL (default 24h)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (defaults to KUBECONFIG env / ~/.kube/config)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func runBootstrap(ctx context.Context, clusterName, namespace string, labelsRaw []string, ttl time.Duration, kubeconfigPath string) error {
	// Build management-cluster client.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	// Parse labels.
	labels := map[string]string{}
	for _, kv := range labelsRaw {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid label %q (expected key=value)", kv)
		}
		labels[parts[0]] = parts[1]
	}

	// Generate a cryptographically random 32-byte token.
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return fmt.Errorf("generate token: %w", err)
	}
	rawToken := hex.EncodeToString(rawBytes)

	// Store only the SHA-256 hash.
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	expiresAt := metav1.NewTime(time.Now().Add(ttl))

	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterName,
			Labels: labels,
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
			Bootstrap: &kaprov1alpha1.MemberClusterBootstrapSpec{
				TokenHash: tokenHash,
				ExpiresAt: &expiresAt,
			},
		},
	}

	if err := c.Create(ctx, mc); err != nil {
		return fmt.Errorf("create MemberCluster: %w", err)
	}

	fmt.Printf(`
✅ MemberCluster created: %s

Cluster: %s
Expires: %s

⏳ Waiting for operator to provision bootstrap credentials...`,
		clusterName,
		clusterName,
		expiresAt.Format(time.RFC3339),
	)

	// Poll for the operator to provision the bootstrap Secret.
	// The operator sets status.bootstrap.issuedBootstrapKubeconfig once the SA token Secret is ready.
	secretName, token, caBundle := "", "", ""
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		fmt.Print(".")

		var check kaprov1alpha1.MemberCluster
		if err := c.Get(ctx, types.NamespacedName{Name: clusterName}, &check); err != nil {
			continue
		}
		if check.Status.Bootstrap != nil && check.Status.Bootstrap.IssuedBootstrapKubeconfig != "" {
			secretName = check.Status.Bootstrap.IssuedBootstrapKubeconfig
			break
		}
	}
	fmt.Println()

	if secretName != "" {
		// Read bootstrap kubeconfig Secret and extract SA token for the join command.
		var bsSecret corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: "kapro-system", Name: secretName}, &bsSecret); err == nil {
			if t, parseErr := extractTokenFromKubeconfig(bsSecret.Data["kubeconfig"]); parseErr == nil {
				token = t
			}
		}
		// Build CA bundle from hub config.
		hubRawCA := cfg.CAData
		if len(hubRawCA) == 0 && cfg.CAFile != "" {
			hubRawCA, _ = os.ReadFile(cfg.CAFile)
		}
		caBundle = base64.StdEncoding.EncodeToString(hubRawCA)
	}

	if token != "" {
		fmt.Printf(`✅ Bootstrap credentials provisioned.

🔗 Join command (paste on the spoke cluster):

   kubectl config use-context <spoke-context>
   kapro spoke join \
     --name %s \
     --hub %s \
     --token %s \
     --ca-bundle %s

⚠️  If --hub URL is not reachable from the spoke, override with the spoke-reachable URL.
    Check spoke registration: kubectl get membercluster %s
`,
			clusterName, cfg.Host, token, caBundle, clusterName)
	} else {
		fmt.Printf(`⏳ Operator has not provisioned bootstrap credentials yet.
   Run once it does:
     kapro spoke install --cluster-name %s --hub-url <spoke-reachable-hub-url>
   Or check: kubectl get membercluster %s -o yaml
`, clusterName, clusterName)
	}
	return nil
}

// ─── kapro cluster join ───────────────────────────────────────────────────────

// newClusterJoinCmd is the one-shot pipeline command:
// registers the cluster on hub AND installs the controller on spoke in one call.
func newClusterJoinCmd() *cobra.Command {
	var (
		clusterName      string
		hubKubeconfig    string
		spokeKubeconfig  string
		hubURL           string
		labelsRaw        []string
		ttl              time.Duration
		image            string
		gcpServiceAccount string
		wait             bool
	)

	cmd := &cobra.Command{
		Use:   "join",
		Short: "Register a cluster on hub and install the controller on spoke in one step",
		Long: `Join is the one-shot command for pipelines.

It combines:
  1. kapro cluster bootstrap  — creates MemberCluster on hub
  2. kapro spoke install      — installs cluster-controller on spoke
  3. Wait for phase=Converged (with --wait)

Both hub and spoke kubeconfigs are required (or use current context + KUBECONFIG).
Use 'kapro spoke join' instead when the pipeline only has access to one cluster.

Example (Azure DevOps / GitHub Actions):
  kapro cluster join \
    --name spoke-de \
    --hub-kubeconfig $HUB_KUBECONFIG \
    --spoke-kubeconfig $SPOKE_KUBECONFIG \
    --hub-url https://hub.internal.com \
    --labels tier=prod,country=de \
    --wait`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterJoin(cmd.Context(), clusterName, hubKubeconfig, spokeKubeconfig, hubURL, image, gcpServiceAccount, labelsRaw, ttl, wait)
		},
	}

	cmd.Flags().StringVar(&clusterName, "name", "", "Cluster name (must be unique; used as MemberCluster name)")
	cmd.Flags().StringVar(&hubKubeconfig, "hub-kubeconfig", "", "Hub kubeconfig (defaults to KUBECONFIG / ~/.kube/config)")
	cmd.Flags().StringVar(&spokeKubeconfig, "spoke-kubeconfig", "", "Spoke kubeconfig (defaults to current KUBECONFIG context)")
	cmd.Flags().StringVar(&hubURL, "hub-url", "", "Spoke-reachable hub API URL (overrides kubeconfig host; required for kind)")
	cmd.Flags().StringSliceVar(&labelsRaw, "labels", nil, "Labels to add to MemberCluster (key=value, comma-separated)")
	cmd.Flags().DurationVar(&ttl, "ttl", 1*time.Hour, "Bootstrap token TTL")
	cmd.Flags().StringVar(&image, "image", "ghcr.io/vinnxcapital-gif/kapro/cluster-controller:latest", "cluster-controller image")
	cmd.Flags().StringVar(&gcpServiceAccount, "gcp-service-account", "", "GCP service account email for Workload Identity (GKE only, e.g. kapro-cc@project.iam.gserviceaccount.com)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for MemberCluster phase=Converged")

	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func runClusterJoin(ctx context.Context, clusterName, hubKubeconfigPath, spokeKubeconfigPath, hubURL, image, gcpServiceAccount string, labelsRaw []string, ttl time.Duration, waitConverged bool) error {
	// ── 1. Build hub client ──────────────────────────────────────────────────────
	hubLoadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if hubKubeconfigPath != "" {
		hubLoadingRules.ExplicitPath = hubKubeconfigPath
	}
	hubClientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		hubLoadingRules, &clientcmd.ConfigOverrides{},
	)
	hubCfg, err := hubClientConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("load hub kubeconfig: %w", err)
	}
	hubClient, err := client.New(hubCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create hub client: %w", err)
	}

	// ── 2. Parse labels ──────────────────────────────────────────────────────────
	labels := map[string]string{}
	for _, kv := range labelsRaw {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid label %q (expected key=value)", kv)
		}
		labels[parts[0]] = parts[1]
	}

	// ── 3. Create MemberCluster on hub ───────────────────────────────────────────
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return fmt.Errorf("generate bootstrap nonce: %w", err)
	}
	hash := sha256.Sum256(rawBytes)
	tokenHash := hex.EncodeToString(hash[:])
	expiresAt := metav1.NewTime(time.Now().Add(ttl))

	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Labels: labels},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
			Bootstrap: &kaprov1alpha1.MemberClusterBootstrapSpec{
				TokenHash: tokenHash,
				ExpiresAt: &expiresAt,
			},
		},
	}
	if err := hubClient.Create(ctx, mc); err != nil {
		return fmt.Errorf("create MemberCluster %q: %w", clusterName, err)
	}
	fmt.Printf("✅ MemberCluster %q created on hub\n", clusterName)

	var bootstrapToken string
	if gcpServiceAccount != "" {
		// ── GCP mode: apply hub-side RBAC; skip bootstrap polling and token extraction ─
		fmt.Printf("⚡ GCP mode: applying hub RBAC for GSA %q (skipping bootstrap credentials poll)\n", gcpServiceAccount)
		for _, obj := range buildHubGCPManifests(clusterName, gcpServiceAccount) {
			if err := applyObject(ctx, hubClient, obj); err != nil {
				return fmt.Errorf("apply hub GCP RBAC %T %q: %w", obj, obj.(metav1.Object).GetName(), err)
			}
			fmt.Printf("  ✅ hub: %T/%s\n", obj, obj.(metav1.Object).GetName())
		}
	} else {
		// ── 4. Poll for bootstrap Secret provisioned (operator-driven) ───────────────
		fmt.Print("⏳ Waiting for operator to provision bootstrap credentials")
		var bootstrapSecretName string
		for i := 0; i < 30; i++ {
			time.Sleep(2 * time.Second)
			fmt.Print(".")
			var check kaprov1alpha1.MemberCluster
			if err := hubClient.Get(ctx, types.NamespacedName{Name: clusterName}, &check); err != nil {
				continue
			}
			if check.Status.Bootstrap != nil && check.Status.Bootstrap.IssuedBootstrapKubeconfig != "" {
				bootstrapSecretName = check.Status.Bootstrap.IssuedBootstrapKubeconfig
				break
			}
		}
		fmt.Println()
		if bootstrapSecretName == "" {
			return fmt.Errorf("timed out waiting for bootstrap credentials — is the kapro operator running?\n" +
				"  Check: kubectl get deploy kapro-operator -n kapro-system")
		}
		fmt.Printf("✅ Bootstrap credentials provisioned: %s\n", bootstrapSecretName)

		// ── 5. Extract SA token ───────────────────────────────────────────────────────
		var bsSecret corev1.Secret
		if err := hubClient.Get(ctx, types.NamespacedName{
			Namespace: "kapro-system",
			Name:      bootstrapSecretName,
		}, &bsSecret); err != nil {
			return fmt.Errorf("read bootstrap Secret %q: %w", bootstrapSecretName, err)
		}
		bootstrapToken, err = extractTokenFromKubeconfig(bsSecret.Data["kubeconfig"])
		if err != nil {
			return fmt.Errorf("extract bootstrap token: %w", err)
		}
	}

	hubRawCA := hubCfg.CAData
	if len(hubRawCA) == 0 && hubCfg.CAFile != "" {
		hubRawCA, _ = os.ReadFile(hubCfg.CAFile)
	}
	hubCABundle := base64.StdEncoding.EncodeToString(hubRawCA)

	effectiveHubURL := hubURL
	if effectiveHubURL == "" {
		effectiveHubURL = hubCfg.Host
	}

	// ── 6. Build + apply spoke manifests ────────────────────────────────────────
	manifests := buildSpokeManifests(clusterName, effectiveHubURL, hubCABundle, bootstrapToken, image, gcpServiceAccount)

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

	fmt.Println("🚀 Installing cluster-controller on spoke...")
	for _, obj := range manifests {
		if err := applyObject(ctx, spokeClient, obj); err != nil {
			return fmt.Errorf("apply %T %q: %w", obj, obj.(metav1.Object).GetName(), err)
		}
		fmt.Printf("  ✅ %T/%s\n", obj, obj.(metav1.Object).GetName())
	}
	fmt.Printf("✅ kapro-cluster-controller installed on %s\n", clusterName)

	if !waitConverged {
		fmt.Printf("   Monitor: kubectl get membercluster %s\n", clusterName)
		return nil
	}

	// ── 7. Wait for phase=Converged ──────────────────────────────────────────────
	fmt.Printf("⏳ Waiting for %s to converge", clusterName)
	for i := 0; i < 60; i++ {
		time.Sleep(5 * time.Second)
		fmt.Print(".")
		var latest kaprov1alpha1.MemberCluster
		if err := hubClient.Get(ctx, types.NamespacedName{Name: clusterName}, &latest); err != nil {
			continue
		}
		if latest.Status.Phase == kaprov1alpha1.ClusterPhaseConverged {
			fmt.Printf("\n✅ %s is Converged 🎉\n", clusterName)
			return nil
		}
		if latest.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
			fmt.Printf("\n❌ %s entered Failed phase\n", clusterName)
			return fmt.Errorf("cluster %q failed to converge: check operator and cluster-controller logs", clusterName)
		}
	}
	fmt.Printf("\n⚠️  Timed out waiting for Converged — check: kubectl get membercluster %s\n", clusterName)
	return nil
}

// ─── kapro get ────────────────────────────────────────────────────────────────

func newGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <releases|syncs>",
		Short: "Display Kapro resources",
	}
	cmd.AddCommand(newGetReleasesCmd())
	cmd.AddCommand(newGetSyncsCmd())
	return cmd
}

func newGetReleasesCmd() *cobra.Command {
	var (
		namespace     string
		allNamespaces bool
		kubeconfig    string
	)
	cmd := &cobra.Command{
		Use:   "releases",
		Short: "List Release objects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGetReleases(cmd.Context(), namespace, allNamespaces, kubeconfig)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace (empty = all namespaces)")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List across all namespaces")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runGetReleases(ctx context.Context, namespace string, allNamespaces bool, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	var list kaprov1alpha1.ReleaseList
	opts := listOpts(namespace, allNamespaces)
	if err := c.List(ctx, &list, opts...); err != nil {
		return fmt.Errorf("list releases: %w", err)
	}
	if len(list.Items) == 0 {
		fmt.Println("No releases found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tARTIFACT\tPHASE\tAGE")
	for _, r := range list.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			r.Name, r.Spec.Artifact, r.Status.Phase, age(r.CreationTimestamp.Time))
	}
	return w.Flush()
}

func newGetSyncsCmd() *cobra.Command {
	var (
		namespace     string
		allNamespaces bool
		kubeconfig    string
	)
	cmd := &cobra.Command{
		Use:   "syncs",
		Short: "List Sync objects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGetSyncs(cmd.Context(), namespace, allNamespaces, kubeconfig)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace (empty = all namespaces)")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List across all namespaces")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runGetSyncs(ctx context.Context, namespace string, allNamespaces bool, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	var list kaprov1alpha1.SyncList
	opts := listOpts(namespace, allNamespaces)
	if err := c.List(ctx, &list, opts...); err != nil {
		return fmt.Errorf("list syncs: %w", err)
	}
	if len(list.Items) == 0 {
		fmt.Println("No syncs found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tRELEASE\tENVIRONMENT\tPHASE\tAGE")
	for _, s := range list.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.Name, s.Spec.ReleaseRef, s.Spec.EnvironmentRef, s.Status.Phase, age(s.CreationTimestamp.Time))
	}
	return w.Flush()
}

// ─── kapro approve ────────────────────────────────────────────────────────────

func newApproveCmd() *cobra.Command {
	var (
		namespace  string
		comment    string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "approve <sync-name>",
		Short: "Approve a Sync waiting for human gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApprove(cmd.Context(), args[0], namespace, comment, kubeconfig)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the Sync")
	cmd.Flags().StringVar(&comment, "comment", "", "Optional approval comment (required for bypass)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runApprove(ctx context.Context, syncName, namespace, comment, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	var sync kaprov1alpha1.Sync
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: syncName}, &sync); err != nil {
		return fmt.Errorf("get sync %q: %w", syncName, err)
	}

	if sync.Status.Phase != kaprov1alpha1.SyncPhaseWaitingApproval {
		fmt.Printf("⚠️  Sync %q is in phase %q (not WaitingApproval) — approving anyway.\n",
			syncName, sync.Status.Phase)
	}

	approvalName := syncName + "-approval"
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name:      approvalName,
			Namespace: namespace,
			// Labels required by ApprovalGate's client.MatchingLabels query.
			Labels: map[string]string{
				"kapro.io/release":     sync.Spec.ReleaseRef,
				"kapro.io/environment": sync.Spec.EnvironmentRef,
			},
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:           kaprov1alpha1.ApprovalKindSync,
			Ref:            syncName,
			Release:        sync.Spec.ReleaseRef,
			EnvironmentRef: sync.Spec.EnvironmentRef,
			Comment:        comment,
			// approvedBy will be overwritten by the ApprovalMutator webhook
			// with the real Kubernetes username from the admission request.
		},
	}

	if err := c.Create(ctx, approval); err != nil {
		return fmt.Errorf("create approval: %w", err)
	}

	fmt.Printf("✅ Approval created: %s/%s\n", namespace, approvalName)
	fmt.Printf("   Sync:        %s\n", syncName)
	fmt.Printf("   Release:     %s\n", sync.Spec.ReleaseRef)
	fmt.Printf("   Environment: %s\n", sync.Spec.EnvironmentRef)
	return nil
}

// ─── kapro rollback ───────────────────────────────────────────────────────────

func newRollbackCmd() *cobra.Command {
	var (
		toDigest   string
		namespace  string
		envs       []string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "rollback <release-name>",
		Short: "Roll back a release to a previous OCI digest",
		Long: `Create a new Release pointing at a previous OCI digest.

The original Release is never modified (immutable). A new Artifact CR is
created with the provided digest and a new Release is created referencing it.

Use --env to target only specific clusters (hotfix / partial rollback).

Examples:
  kapro rollback my-release --to sha256:abc123def456
  kapro rollback my-release --to sha256:abc123def456 --env de-prod,fi-prod`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollback(cmd.Context(), args[0], toDigest, namespace, envs, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&toDigest, "to", "", "OCI digest to roll back to (required)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the Release")
	cmd.Flags().StringArrayVar(&envs, "env", nil, "Restrict rollback to specific environments (repeatable: --env de-prod --env fi-prod)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func runRollback(ctx context.Context, releaseName, toDigest, namespace string, envs []string, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	// Fetch the original Release.
	var orig kaprov1alpha1.Release
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: releaseName}, &orig); err != nil {
		return fmt.Errorf("get release %q: %w", releaseName, err)
	}

	// Fetch the Artifact to extract the OCI repository URL.
	var origArtifact kaprov1alpha1.Artifact
	if err := c.Get(ctx, types.NamespacedName{Name: orig.Spec.Artifact}, &origArtifact); err != nil {
		return fmt.Errorf("get artifact %q: %w", orig.Spec.Artifact, err)
	}
	if len(origArtifact.Spec.Sources) == 0 || origArtifact.Spec.Sources[0].OCI == nil {
		return fmt.Errorf("artifact %q has no OCI source", orig.Spec.Artifact)
	}
	repo := origArtifact.Spec.Sources[0].OCI.Repository

	// Derive short suffix from digest for readable names.
	suffix := shortHash(toDigest)

	// Create a rollback Artifact CR pinned at the requested digest.
	rbArtifactName := orig.Spec.Artifact + "-rb-" + suffix
	rbArtifact := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: rbArtifactName},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{
				{
					Type: "oci",
					OCI: &kaprov1alpha1.OCIRef{
						Repository: repo,
						Tag:        "rollback-" + suffix,
						Digest:     toDigest,
					},
				},
			},
			Metadata: kaprov1alpha1.ArtifactMeta{
				ReleasedBy:  "kapro-cli-rollback",
				Description: "Rollback from " + releaseName + " to " + toDigest,
			},
		},
	}
	if err := c.Create(ctx, rbArtifact); err != nil {
		return fmt.Errorf("create rollback artifact: %w", err)
	}

	// Create a rollback Release with the same pipelines but the rollback Artifact.
	rbReleaseName := releaseName + "-rb-" + suffix
	rbSpec := kaprov1alpha1.ReleaseSpec{
		Artifact:    rbArtifactName,
		Pipelines:   orig.Spec.Pipelines,
		AppKey:      orig.Spec.AppKey,
		DerivedFrom: releaseName,
	}
	if len(envs) > 0 {
		rbSpec.Scope = &kaprov1alpha1.ReleaseScope{Environments: envs}
	}
	rbRelease := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbReleaseName,
			Namespace: namespace,
			Annotations: map[string]string{
				"kapro.io/rollback-from":   releaseName,
				"kapro.io/rollback-digest": toDigest,
			},
		},
		Spec: rbSpec,
	}
	if err := c.Create(ctx, rbRelease); err != nil {
		// Clean up the artifact we just created to avoid orphan objects.
		_ = c.Delete(ctx, rbArtifact)
		return fmt.Errorf("create rollback release: %w", err)
	}

	fmt.Printf("✅ Rollback release created: %s/%s\n", namespace, rbReleaseName)
	fmt.Printf("   Original release: %s\n", releaseName)
	fmt.Printf("   Rollback digest:  %s\n", toDigest)
	fmt.Printf("   Rollback artifact: %s\n", rbArtifactName)
	if len(envs) > 0 {
		fmt.Printf("   Scoped to envs:   %s\n", strings.Join(envs, ", "))
	}
	fmt.Printf("\nMonitor progress:\n  kapro get syncs -n %s\n", namespace)
	return nil
}

// ─── shared helpers ───────────────────────────────────────────────────────────

func buildClient(kubeconfigPath string) (client.Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	return c, nil
}

func listOpts(namespace string, allNamespaces bool) []client.ListOption {
	if allNamespaces || namespace == "" {
		return nil
	}
	return []client.ListOption{client.InNamespace(namespace)}
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

// ─── kapro artifact ───────────────────────────────────────────────────────────

func newArtifactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Manage Kapro Artifact CRs",
	}
	cmd.AddCommand(newArtifactPushCmd())
	return cmd
}

func newArtifactPushCmd() *cobra.Command {
	var (
		name        string
		repository  string
		tag         string
		digest      string
		releasedBy  string
		desc        string
		derivedFrom string
		changed     []string
		kubeconfig  string
	)
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Create an Artifact CR from an OCI bundle",
		Long: `Register an OCI bundle as a Kapro Artifact CR.

Run 'flux push artifact' first to push the bundle to the registry, then use
this command to create the Artifact CR that Kapro releases reference.

Example:
  # Step 1 — push the bundle (flux CLI)
  flux push artifact oci://registry.example.com/my-app:v1.2.3 --path=./bundle

  # Step 2 — register with Kapro
  kapro artifact push \
    --name=my-app-v1.2.3 \
    --repository=registry.example.com/my-app \
    --tag=v1.2.3 \
    --digest=sha256:abc123 \
    --released-by=ci-pipeline`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runArtifactPush(cmd.Context(), name, repository, tag, digest,
				releasedBy, desc, derivedFrom, changed, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Artifact CR name (required)")
	cmd.Flags().StringVar(&repository, "repository", "", "OCI repository URL (required)")
	cmd.Flags().StringVar(&tag, "tag", "", "OCI tag (required)")
	cmd.Flags().StringVar(&digest, "digest", "", "OCI digest sha256:... (required)")
	cmd.Flags().StringVar(&releasedBy, "released-by", "ci", "Who/what released this artifact")
	cmd.Flags().StringVar(&desc, "description", "", "Human-readable description")
	cmd.Flags().StringVar(&derivedFrom, "derived-from", "", "Parent Artifact name (for hotfix lineage)")
	cmd.Flags().StringArrayVar(&changed, "changed", nil, "Changed component (repeatable: --changed svc-a --changed svc-b)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("repository")
	_ = cmd.MarkFlagRequired("tag")
	_ = cmd.MarkFlagRequired("digest")
	return cmd
}

func runArtifactPush(ctx context.Context, name, repository, tag, digest,
	releasedBy, description, derivedFrom string, changed []string, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	artifact := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{
				{
					Type: "oci",
					OCI: &kaprov1alpha1.OCIRef{
						Repository: repository,
						Tag:        tag,
						Digest:     digest,
					},
				},
			},
			Metadata: kaprov1alpha1.ArtifactMeta{
				ReleasedBy:        releasedBy,
				Description:       description,
				DerivedFrom:       derivedFrom,
				ChangedComponents: changed,
			},
		},
	}

	if err := c.Create(ctx, artifact); err != nil {
		return fmt.Errorf("create Artifact: %w", err)
	}

	fmt.Printf("✅ Artifact created: %s\n", name)
	fmt.Printf("   Repository: %s\n", repository)
	fmt.Printf("   Tag:        %s\n", tag)
	fmt.Printf("   Digest:     %s\n", digest)
	if derivedFrom != "" {
		fmt.Printf("   Derived from: %s\n", derivedFrom)
	}
	if len(changed) > 0 {
		fmt.Printf("   Changed:    %s\n", strings.Join(changed, ", "))
	}
	return nil
}

// ─── kapro release ────────────────────────────────────────────────────────────

func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Manage Kapro Releases",
	}
	cmd.AddCommand(newReleaseCreateCmd())
	return cmd
}

func newReleaseCreateCmd() *cobra.Command {
	var (
		name        string
		artifact    string
		pipelines   []string
		scope       []string
		derivedFrom string
		namespace   string
		appKey      string
		kubeconfig  string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Release to deliver an artifact across the fleet",
		Long: `Create a Kapro Release CR that drives progressive delivery.

Examples:
  # Full-fleet release
  kapro release create --name v1.2.3 --artifact my-app-v1.2.3 --pipeline global

  # Hotfix targeting specific clusters only
  kapro release create --name v1.2.3-hotfix --artifact my-app-v1.2.3-hotfix \
    --pipeline global --scope de-prod --scope fi-prod \
    --derived-from v1.2.3`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReleaseCreate(cmd.Context(), name, artifact, pipelines, scope, derivedFrom, namespace, appKey, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Release name (required)")
	cmd.Flags().StringVar(&artifact, "artifact", "", "Artifact CR name to deliver (required)")
	cmd.Flags().StringArrayVar(&pipelines, "pipeline", nil, "Pipeline name (repeatable; required at least once)")
	cmd.Flags().StringArrayVar(&scope, "scope", nil, "Restrict to environment (repeatable: --scope de-prod --scope fi-prod)")
	cmd.Flags().StringVar(&derivedFrom, "derived-from", "", "Parent Release name (for hotfix lineage)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace for the Release")
	cmd.Flags().StringVar(&appKey, "app-key", "", "App key for MemberCluster version tracking (defaults to artifact name)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("artifact")
	return cmd
}

func runReleaseCreate(ctx context.Context, name, artifact string, pipelines, scope []string,
	derivedFrom, namespace, appKey, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	if len(pipelines) == 0 {
		return fmt.Errorf("at least one --pipeline is required")
	}

	refs := make([]kaprov1alpha1.ReleasePipelineRef, 0, len(pipelines))
	for i, p := range pipelines {
		refs = append(refs, kaprov1alpha1.ReleasePipelineRef{
			Name:     fmt.Sprintf("p%d", i+1),
			Pipeline: p,
		})
	}

	spec := kaprov1alpha1.ReleaseSpec{
		Artifact:    artifact,
		Pipelines:   refs,
		AppKey:      appKey,
		DerivedFrom: derivedFrom,
	}
	if len(scope) > 0 {
		spec.Scope = &kaprov1alpha1.ReleaseScope{Environments: scope}
	}

	rel := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: spec,
	}

	if err := c.Create(ctx, rel); err != nil {
		return fmt.Errorf("create Release: %w", err)
	}

	fmt.Printf("✅ Release created: %s/%s\n", namespace, name)
	fmt.Printf("   Artifact:  %s\n", artifact)
	pipelineNames := make([]string, len(pipelines))
	copy(pipelineNames, pipelines)
	fmt.Printf("   Pipelines: %s\n", strings.Join(pipelineNames, ", "))
	if len(scope) > 0 {
		fmt.Printf("   Scope:     %s\n", strings.Join(scope, ", "))
	}
	if derivedFrom != "" {
		fmt.Printf("   Derived from: %s\n", derivedFrom)
	}
	fmt.Printf("\nMonitor progress:\n  kapro get releases -n %s\n", namespace)
	return nil
}

// ─── kapro promote ────────────────────────────────────────────────────────────

func newPromoteCmd() *cobra.Command {
	var (
		releaseName string
		pipeline    string
		stage       string
		envs        []string
		comment     string
		namespace   string
		kubeconfig  string
	)
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Manually advance a stage past its gate",
		Long: `Create Approval CRs for all Syncs in a stage that are waiting for human approval.

This is the bulk equivalent of 'kapro approve' — it targets every environment
in a stage rather than a single Sync by name.

Examples:
  # Approve all environments in canary stage
  kapro promote --release v1.2.3 --pipeline global --stage canary --comment "LGTM"

  # Approve specific environments only
  kapro promote --release v1.2.3 --pipeline global --stage canary \
    --env de-prod --env fi-prod --comment "approved for EU first"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPromote(cmd.Context(), releaseName, pipeline, stage, envs, comment, namespace, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&releaseName, "release", "", "Release name (required)")
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Pipeline name the stage belongs to")
	cmd.Flags().StringVar(&stage, "stage", "", "Stage name (required)")
	cmd.Flags().StringArrayVar(&envs, "env", nil, "Target specific environments only (repeatable)")
	cmd.Flags().StringVar(&comment, "comment", "", "Approval comment (required if bypass was requested)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("release")
	_ = cmd.MarkFlagRequired("stage")
	return cmd
}

func runPromote(ctx context.Context, releaseName, pipeline, stage string, envs []string,
	comment, namespace, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	// List Syncs for this release+stage that are WaitingApproval.
	matchLabels := client.MatchingLabels{
		"kapro.io/release": releaseName,
		"kapro.io/stage":   stage,
	}
	if pipeline != "" {
		matchLabels["kapro.io/pipeline"] = pipeline
	}

	var syncList kaprov1alpha1.SyncList
	if err := c.List(ctx, &syncList, matchLabels, client.Limit(500)); err != nil {
		return fmt.Errorf("list syncs: %w", err)
	}

	// Filter by environment allowlist if provided.
	envSet := make(map[string]struct{}, len(envs))
	for _, e := range envs {
		envSet[e] = struct{}{}
	}

	approved := 0
	skipped := 0
	for i := range syncList.Items {
		s := &syncList.Items[i]
		if s.Status.Phase != kaprov1alpha1.SyncPhaseWaitingApproval {
			skipped++
			continue
		}
		if len(envSet) > 0 {
			if _, ok := envSet[s.Spec.EnvironmentRef]; !ok {
				skipped++
				continue
			}
		}

		approvalName := s.Name + "-approval"
		approval := &kaprov1alpha1.Approval{
			ObjectMeta: metav1.ObjectMeta{
				Name:      approvalName,
				Namespace: namespace,
				Labels: map[string]string{
					"kapro.io/release":     releaseName,
					"kapro.io/environment": s.Spec.EnvironmentRef,
				},
			},
			Spec: kaprov1alpha1.ApprovalSpec{
				Kind:           kaprov1alpha1.ApprovalKindSync,
				Ref:            s.Name,
				Release:        releaseName,
				EnvironmentRef: s.Spec.EnvironmentRef,
				Comment:        comment,
			},
		}
		if err := c.Create(ctx, approval); client.IgnoreAlreadyExists(err) != nil {
			fmt.Printf("⚠️  Failed to create approval for %s: %v\n", s.Spec.EnvironmentRef, err)
			continue
		}
		fmt.Printf("✅ Approved: %s (env: %s)\n", s.Name, s.Spec.EnvironmentRef)
		approved++
	}

	if approved == 0 && skipped == len(syncList.Items) {
		fmt.Printf("ℹ️  No Syncs in WaitingApproval for release=%s stage=%s\n", releaseName, stage)
		return nil
	}
	fmt.Printf("\n%d approval(s) created, %d skipped (not WaitingApproval)\n", approved, skipped)
	return nil
}

// ─── kapro world ──────────────────────────────────────────────────────────────

func newWorldCmd() *cobra.Command {
	var (
		env        string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "world",
		Short: "Fleet-wide status: all clusters, versions, and health",
		Long: `Show a table of every MemberCluster with its current version, phase, and health.

Equivalent to 'kubectl get memberclusters' but formatted for delivery monitoring.

Examples:
  kapro world
  kapro world --env prod`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorld(cmd.Context(), env, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "Filter by Environment name")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runWorld(ctx context.Context, envFilter, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	var list kaprov1alpha1.MemberClusterList
	opts := []client.ListOption{client.Limit(2000)}
	if envFilter != "" {
		opts = append(opts, client.MatchingLabels{"kapro.io/environment": envFilter})
	}
	if err := c.List(ctx, &list, opts...); err != nil {
		return fmt.Errorf("list member clusters: %w", err)
	}
	if len(list.Items) == 0 {
		fmt.Println("No member clusters found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CLUSTER\tPHASE\tHEALTHY\tACTIVE RELEASE\tHEARTBEAT\tAGE")
	for _, mc := range list.Items {
		healthy := "?"
		if mc.Status.Health.AllWorkloadsReady {
			healthy = "true"
		} else if mc.Status.LastHeartbeat != "" {
			healthy = "false"
		}
		heartbeat := "-"
		if mc.Status.LastHeartbeat != "" {
			heartbeat = mc.Status.LastHeartbeat
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			mc.Name,
			mc.Status.Phase,
			healthy,
			mc.Status.ActiveRelease,
			heartbeat,
			age(mc.CreationTimestamp.Time),
		)
	}
	return w.Flush()
}
