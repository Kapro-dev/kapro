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
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
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
	root.AddCommand(newGetCmd())
	root.AddCommand(newApproveCmd())
	root.AddCommand(newRollbackCmd())

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
		Long: `Bootstrap creates a BootstrapToken CR on the management cluster.
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

	cmd.Flags().StringVar(&clusterName, "name", "", "Cluster name (required, must match ManagedCluster name)")
	cmd.Flags().StringVar(&namespace, "namespace", "kapro-system", "Namespace for the BootstrapToken CR")
	cmd.Flags().StringArrayVar(&labelsRaw, "labels", nil, "Labels for the ManagedCluster (key=value)")
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
	btName := clusterName + "-bootstrap"

	bt := &kaprov1alpha1.BootstrapToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:      btName,
			Namespace: namespace,
		},
		Spec: kaprov1alpha1.BootstrapTokenSpec{
			ClusterName: clusterName,
			TokenHash:   tokenHash,
			ExpiresAt:   expiresAt,
			Labels:      labels,
		},
	}

	if err := c.Create(ctx, bt); err != nil {
		return fmt.Errorf("create BootstrapToken: %w", err)
	}

	fmt.Printf(`
✅ BootstrapToken created: %s/%s

Cluster:    %s
Token hash: %s (stored — plaintext never persisted)
Expires:    %s

🔑 Bootstrap Token (keep secret — one-time use):
   %s

Next steps:
  1. On the WORKLOAD cluster, start kapro-cluster-controller with:
       --bootstrap-token=%s
       --management-cluster-url=%s
  
  2. The operator will:
       • Create a scoped ServiceAccount (kapro-cluster-%s)
       • Create ClusterRole scoped to ManagedCluster/%s only
       • Issue a short-lived SA token (1h, auto-renewing)
       • Write credentials to Secret: kapro-system/kapro-cluster-%s-credentials

  3. Check bootstrap status:
       kubectl get bootstraptoken %s -n %s -o yaml
`,
		namespace, btName,
		clusterName,
		tokenHash[:16]+"...",
		expiresAt.Format(time.RFC3339),
		rawToken,
		rawToken,
		cfg.Host,
		clusterName, clusterName, clusterName,
		btName, namespace,
	)

	// Wait briefly and check if operator has already processed it.
	fmt.Print("⏳ Waiting for operator to process token")
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		fmt.Print(".")

		var check kaprov1alpha1.BootstrapToken
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: btName}, &check); err != nil {
			continue
		}
		if check.Status.Used {
			fmt.Printf("\n✅ Token consumed by operator. SA: %s\n", check.Status.IssuedCredentialFor)
			fmt.Printf("   Credentials secret: kapro-system/kapro-cluster-%s-credentials\n", clusterName)
			return nil
		}
	}
	fmt.Println("\n⏳ Operator has not processed the token yet — it will be processed on next reconcile.")
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
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "rollback <release-name>",
		Short: "Roll back a release to a previous OCI digest",
		Long: `Create a new Release pointing at a previous OCI digest.

The original Release is never modified (immutable). A new Artifact CR is
created with the provided digest and a new Release is created referencing it.

Example:
  kapro rollback my-release --to sha256:abc123def456`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollback(cmd.Context(), args[0], toDigest, namespace, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&toDigest, "to", "", "OCI digest to roll back to (required)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the Release")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func runRollback(ctx context.Context, releaseName, toDigest, namespace, kubeconfigPath string) error {
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
	rbRelease := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbReleaseName,
			Namespace: namespace,
			Annotations: map[string]string{
				"kapro.io/rollback-from":   releaseName,
				"kapro.io/rollback-digest": toDigest,
			},
		},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:  rbArtifactName,
			Pipelines: orig.Spec.Pipelines,
			AppKey:    orig.Spec.AppKey,
		},
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
